// Package config defines every user-changeable setting: what it's called on
// the Settings page, its type, limits and default, and how it's checked.
// Network settings have their own type (see network.go) because changing them
// needs a confirm-or-revert step.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Def describes one setting.
type Def struct {
	Key     string `json:"key"`
	Group   string `json:"group"`
	Label   string `json:"label"`
	Help    string `json:"help,omitempty"`
	Type    string `json:"type"` // "int", "bool" or "string"
	Unit    string `json:"unit,omitempty"`
	Min     int    `json:"min,omitempty"`
	Max     int    `json:"max,omitempty"`
	MaxLen  int    `json:"max_len,omitempty"`
	Default any    `json:"default"`
	// Placeholder is shown in an empty text box.
	Placeholder string `json:"placeholder,omitempty"`
}

// Group is a section of the Settings page.
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Groups lists the Settings page sections in order.
var Groups = []Group{
	{"general", "General"},
	{"security", "Sign-in and sessions"},
	{"history", "Run history"},
}

// Defs lists every setting. New milestones add theirs here, and the Settings
// page picks them up automatically.
var Defs = []Def{
	{Key: "general.server_name", Group: "general", Label: "Server name",
		Help: "Shown in the sidebar, in authenticator apps and in alert emails. Leave blank to use this machine's hostname.",
		Type: "string", MaxLen: 64, Default: ""},
	{Key: "general.public_url", Group: "general", Label: "Web address of this server",
		Help: "Used for links in alert emails, like https://backups.example.net/. Leave blank to leave links out.",
		Type: "string", MaxLen: 200, Default: "", Placeholder: "https://backups.example.net/"},
	{Key: "security.session_hours", Group: "security", Label: "Sign out after inactivity",
		Help: "How long a signed-in browser stays signed in without being used.",
		Type: "int", Unit: "hours", Min: 1, Max: 720, Default: 12},
	{Key: "history.client_runs", Group: "history", Label: "Runs kept on each client",
		Help: "Each client keeps this many of its most recent runs and their logs, so nothing is lost while this server is down.",
		Type: "int", Unit: "runs", Min: 20, Max: 10000, Default: 500},
	{Key: "history.client_days", Group: "history", Label: "Days kept on each client",
		Help: "Runs older than this are removed from clients, however many there are.",
		Type: "int", Unit: "days", Min: 7, Max: 3650, Default: 90},
	{Key: "history.server_runs", Group: "history", Label: "Runs kept on this server",
		Help: "Across all clients. The oldest runs and their logs are removed first.",
		Type: "int", Unit: "runs", Min: 100, Max: 100000, Default: 5000},
}

// Lookup returns the definition for key.
func Lookup(key string) (Def, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// ValidationError is shown to the user as-is.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// Clean checks a value from the browser and returns it in its stored form.
func Clean(key string, raw json.RawMessage) (any, error) {
	d, ok := Lookup(key)
	if !ok {
		return nil, invalid("Unknown setting %q.", key)
	}
	switch d.Type {
	case "int":
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil || n != float64(int(n)) {
			return nil, invalid("%s must be a whole number.", d.Label)
		}
		v := int(n)
		if v < d.Min || v > d.Max {
			return nil, invalid("%s must be between %d and %d.", d.Label, d.Min, d.Max)
		}
		return v, nil
	case "bool":
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, invalid("%s must be on or off.", d.Label)
		}
		return b, nil
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, invalid("%s must be text.", d.Label)
		}
		s = strings.TrimSpace(s)
		if d.MaxLen > 0 && len([]rune(s)) > d.MaxLen {
			return nil, invalid("%s can be at most %d characters.", d.Label, d.MaxLen)
		}
		if strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return nil, invalid("%s can't contain control characters.", d.Label)
		}
		if key == "general.public_url" && s != "" {
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s, " <>\"") {
				return nil, invalid("Enter the address like https://backups.example.net/, or leave it blank.")
			}
			if !strings.HasSuffix(s, "/") {
				s += "/"
			}
		}
		return s, nil
	}
}
