package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Client statuses.
const (
	ClientSettingUp      = "setting-up"
	ClientReady          = "ready"
	ClientError          = "error"
	ClientUnreachable    = "unreachable"
	ClientHostKeyChanged = "host-key-changed"
)

// Client is a machine managed over SSH.
type Client struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	Port           int    `json:"port"`
	HostKey        string `json:"host_key"`
	Status         string `json:"status"`
	StatusDetail   string `json:"status_detail"`
	OfferedKey     string `json:"offered_key"`
	OSID           string `json:"os_id"`
	OSPretty       string `json:"os_pretty"`
	OSCodename     string `json:"os_codename"`
	Arch           string `json:"arch"`
	Hostname       string `json:"hostname"`
	SystemdVersion string `json:"systemd_version"`
	ClientVersion  string `json:"client_version"`
	RunnerVersion  string `json:"runner_version"`
	ServerHere     bool   `json:"server_here"`
	LastContact    int64  `json:"last_contact"`
	CreatedAt      int64  `json:"created_at"`
}

// ErrNotFound means the record doesn't exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate means a unique field is already taken.
type ErrDuplicate struct{ Field string }

func (e *ErrDuplicate) Error() string { return "duplicate " + e.Field }

const clientCols = `id, name, address, port, host_key, status, status_detail, offered_key, os_id, os_pretty,
	os_codename, arch, hostname, systemd_version, client_version, runner_version, server_here, last_contact, created_at`

func scanClient(row interface{ Scan(...any) error }) (*Client, error) {
	var c Client
	var here int
	err := row.Scan(&c.ID, &c.Name, &c.Address, &c.Port, &c.HostKey, &c.Status, &c.StatusDetail, &c.OfferedKey,
		&c.OSID, &c.OSPretty, &c.OSCodename, &c.Arch, &c.Hostname, &c.SystemdVersion, &c.ClientVersion,
		&c.RunnerVersion, &here, &c.LastContact, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.ServerHere = here != 0
	return &c, err
}

func dupError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "clients.name"):
		return &ErrDuplicate{"name"}
	case strings.Contains(msg, "clients.address"):
		return &ErrDuplicate{"address"}
	}
	return err
}

// CreateClient inserts a client.
func (s *Store) CreateClient(c *Client) error {
	c.CreatedAt = s.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO clients (`+clientCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Address, c.Port, c.HostKey, c.Status, c.StatusDetail, c.OfferedKey, c.OSID, c.OSPretty,
		c.OSCodename, c.Arch, c.Hostname, c.SystemdVersion, c.ClientVersion, c.RunnerVersion, boolInt(c.ServerHere),
		c.LastContact, c.CreatedAt)
	return dupError(err)
}

// SaveClient updates every field of a client.
func (s *Store) SaveClient(c *Client) error {
	res, err := s.db.Exec(`UPDATE clients SET name=?, address=?, port=?, host_key=?, status=?, status_detail=?,
		offered_key=?, os_id=?, os_pretty=?, os_codename=?, arch=?, hostname=?, systemd_version=?, client_version=?,
		runner_version=?, server_here=?, last_contact=? WHERE id=?`,
		c.Name, c.Address, c.Port, c.HostKey, c.Status, c.StatusDetail, c.OfferedKey, c.OSID, c.OSPretty, c.OSCodename,
		c.Arch, c.Hostname, c.SystemdVersion, c.ClientVersion, c.RunnerVersion, boolInt(c.ServerHere), c.LastContact, c.ID)
	if err != nil {
		return dupError(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetClient returns one client.
func (s *Store) GetClient(id string) (*Client, error) {
	return scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE id = ?`, id))
}

// ListClients returns every client, by name.
func (s *Store) ListClients() ([]*Client, error) {
	rows, err := s.db.Query(`SELECT ` + clientCols + ` FROM clients ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Client{}
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteClient removes a client.
func (s *Store) DeleteClient(id string) error {
	_, err := s.db.Exec(`DELETE FROM clients WHERE id = ?`, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
