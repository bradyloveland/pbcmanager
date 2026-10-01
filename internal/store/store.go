// Package store keeps the application's state in SQLite: settings, the admin
// account and sessions. Schema changes are numbered migrations applied at
// startup.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/secret"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the database. It's safe for concurrent use.
type Store struct {
	db  *sql.DB
	box *secret.Box
	Now func() time.Time
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations.
func Open(path string, box *secret.Box) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises writes, so there's never a "database is
	// locked" error inside the app. The workload is small.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, box: box, Now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the newest applied migration.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	current, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad name", base)
		}
		if v <= current {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, s.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- settings

// GetSetting decodes the setting into dst. It reports false if it isn't set.
func (s *Store) GetSetting(key string, dst any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

// SetSetting stores v as JSON.
func (s *Store) SetSetting(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(raw))
	return err
}

// SetSettings stores several settings in one transaction.
func (s *Store) SetSettings(values map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range values {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ------------------------------------------------------------------- admin

// Admin is the single administrator account, with the TOTP secret decrypted.
type Admin struct {
	Username      string
	PasswordHash  string
	TOTPSecret    string
	TOTPLastStep  int64
	RecoveryCodes []string // SHA-256 hashes
}

// ErrNoAdmin means setup hasn't been completed.
var ErrNoAdmin = errors.New("no admin account yet")

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) loadAdmin(q querier) (*Admin, error) {
	var a Admin
	var sealed, codes string
	err := q.QueryRow(`SELECT username, password_hash, totp_secret, totp_last_step, recovery_codes
		FROM admin WHERE id = 1`).Scan(&a.Username, &a.PasswordHash, &sealed, &a.TOTPLastStep, &codes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAdmin
	}
	if err != nil {
		return nil, err
	}
	if a.TOTPSecret, err = s.box.Open(sealed); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(codes), &a.RecoveryCodes); err != nil {
		return nil, err
	}
	return &a, nil
}

// GetAdmin returns the admin account, or ErrNoAdmin.
func (s *Store) GetAdmin() (*Admin, error) { return s.loadAdmin(s.db) }

// HasAdmin reports whether setup has been completed.
func (s *Store) HasAdmin() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admin`).Scan(&n)
	return n > 0, err
}

// CreateAdmin creates the account. It fails if one already exists, so two
// people racing through setup can't both win.
func (s *Store) CreateAdmin(username, passwordHash string) error {
	_, err := s.db.Exec(`INSERT INTO admin (id, username, password_hash, updated_at) VALUES (1, ?, ?, ?)`,
		username, passwordHash, s.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errors.New("setup has already been completed")
	}
	return err
}

// UpdateAdmin loads the account, lets fn change it and saves it, all in one
// transaction. If fn returns an error nothing is saved.
func (s *Store) UpdateAdmin(fn func(*Admin) error) (*Admin, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := s.loadAdmin(tx)
	if err != nil {
		return nil, err
	}
	if err := fn(a); err != nil {
		return nil, err
	}
	if a.RecoveryCodes == nil {
		a.RecoveryCodes = []string{}
	}
	codes, _ := json.Marshal(a.RecoveryCodes)
	if _, err := tx.Exec(`UPDATE admin SET username = ?, password_hash = ?, totp_secret = ?,
		totp_last_step = ?, recovery_codes = ?, updated_at = ? WHERE id = 1`,
		a.Username, a.PasswordHash, s.box.Seal(a.TOTPSecret), a.TOTPLastStep, string(codes), s.Now().Unix()); err != nil {
		return nil, err
	}
	return a, tx.Commit()
}

// ---------------------------------------------------------------- sessions

// HashToken returns the value sessions are stored under.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession records a new session for a token.
func (s *Store) CreateSession(token, ip, userAgent string) error {
	now := s.Now().Unix()
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, created_at, last_seen, ip, user_agent) VALUES (?, ?, ?, ?, ?)`,
		HashToken(token), now, now, ip, userAgent)
	return err
}

// CheckSession reports whether token is a live session, and refreshes its
// idle timer. Sessions idle for longer than idle are deleted.
func (s *Store) CheckSession(token string, idle time.Duration) (bool, error) {
	if token == "" {
		return false, nil
	}
	h := HashToken(token)
	var lastSeen int64
	err := s.db.QueryRow(`SELECT last_seen FROM sessions WHERE token_hash = ?`, h).Scan(&lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := s.Now().Unix()
	if now-lastSeen > int64(idle.Seconds()) {
		_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, h)
		return false, err
	}
	// Writing on every request is wasteful; a minute's precision is plenty.
	if now-lastSeen >= 60 {
		if _, err := s.db.Exec(`UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, now, h); err != nil {
			return false, err
		}
	}
	return true, nil
}

// DeleteSession signs a token out.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

// DeleteOtherSessions signs out every session except keep (which may be "").
func (s *Store) DeleteOtherSessions(keep string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash != ?`, HashToken(keep))
	return err
}

// PurgeSessions deletes sessions idle for longer than idle.
func (s *Store) PurgeSessions(idle time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE last_seen < ?`, s.Now().Add(-idle).Unix())
	return err
}

// CountSessions returns the number of stored sessions.
func (s *Store) CountSessions() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}

// RawAdminTOTP returns the stored (encrypted) TOTP column. Tests use it to
// check nothing is kept in plain text.
func (s *Store) RawAdminTOTP() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT totp_secret FROM admin WHERE id = 1`).Scan(&v)
	return v, err
}
