package store

import "strings"

// Alert is an email the server decided to send.
type Alert struct {
	ID        int64  `json:"id"`
	Key       string `json:"-"`
	Kind      string `json:"kind"` // failed, succeeded, missed, unreachable, reachable, test
	ClientID  string `json:"client_id"`
	JobID     string `json:"job_id"`
	RunID     string `json:"run_id"`
	Subject   string `json:"subject"`
	CreatedAt int64  `json:"created_at"`
	SentAt    int64  `json:"sent_at"`
	Error     string `json:"error"`
}

// AddAlert records an alert. It returns false (and does nothing) if one with
// the same key exists, so each alert is sent once.
func (s *Store) AddAlert(a *Alert) (bool, error) {
	a.CreatedAt = s.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO alerts (key, kind, client_id, job_id, run_id, subject, created_at) VALUES (?,?,?,?,?,?,?)`,
		a.Key, a.Kind, a.ClientID, a.JobID, a.RunID, a.Subject, a.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return false, nil
		}
		return false, err
	}
	a.ID, _ = res.LastInsertId()
	return true, nil
}

// AlertDelivered records whether an alert's email went out.
func (s *Store) AlertDelivered(id int64, sendErr error) error {
	if sendErr != nil {
		msg := sendErr.Error()
		if len(msg) > 400 {
			msg = msg[:400]
		}
		_, err := s.db.Exec(`UPDATE alerts SET error = ? WHERE id = ?`, msg, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE alerts SET sent_at = ?, error = '' WHERE id = ?`, s.Now().Unix(), id)
	return err
}

// HasAlert reports whether an alert with this key was recorded.
func (s *Store) HasAlert(key string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE key = ?`, key).Scan(&n)
	return n > 0, err
}

// ListAlerts returns the most recent alerts.
func (s *Store) ListAlerts(limit int) ([]*Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, key, kind, client_id, job_id, run_id, subject, created_at, sent_at, error
		FROM alerts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Key, &a.Kind, &a.ClientID, &a.JobID, &a.RunID, &a.Subject, &a.CreatedAt, &a.SentAt, &a.Error); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// PruneAlerts keeps the newest keep alerts.
func (s *Store) PruneAlerts(keep int) error {
	_, err := s.db.Exec(`DELETE FROM alerts WHERE id NOT IN (SELECT id FROM alerts ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

// Seal encrypts a secret for storage in a setting.
func (s *Store) Seal(plain string) string { return s.box.Seal(plain) }

// Open decrypts a secret sealed with Seal.
func (s *Store) Open(sealed string) (string, error) { return s.box.Open(sealed) }
