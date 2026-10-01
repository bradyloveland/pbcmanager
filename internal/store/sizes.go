package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// PutSize stores a cached size.
func (s *Store) PutSize(kind, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sizes (kind, key, data, updated) VALUES (?,?,?,?)
		ON CONFLICT(kind, key) DO UPDATE SET data = excluded.data, updated = excluded.updated`, kind, key, string(raw), s.Now().Unix())
	return err
}

// GetSize reads a cached size into dst. It reports false if there's none.
func (s *Store) GetSize(kind, key string, dst any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT data FROM sizes WHERE kind = ? AND key = ?`, kind, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

// ListSizes returns every cached size of a kind, by key.
func (s *Store) ListSizes(kind string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT key, data FROM sizes WHERE kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// DeleteSize removes a cached size.
func (s *Store) DeleteSize(kind, key string) error {
	_, err := s.db.Exec(`DELETE FROM sizes WHERE kind = ? AND key = ?`, kind, key)
	return err
}
