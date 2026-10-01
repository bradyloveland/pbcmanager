package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanSettings(t *testing.T) {
	ok := []struct {
		key, raw string
		want     any
	}{
		{"security.session_hours", "24", 24},
		{"general.server_name", `"  nas  "`, "nas"},
		{"general.server_name", `""`, ""},
	}
	for _, c := range ok {
		got, err := Clean(c.key, json.RawMessage(c.raw))
		if err != nil || got != c.want {
			t.Errorf("%s=%s: got %v, %v", c.key, c.raw, got, err)
		}
	}
	bad := []struct{ key, raw, msg string }{
		{"security.session_hours", "0", "between 1 and 720"},
		{"security.session_hours", "1.5", "whole number"},
		{"security.session_hours", `"12"`, "whole number"},
		{"general.server_name", `"` + strings.Repeat("x", 65) + `"`, "at most 64"},
		{"general.server_name", `"a\u0007b"`, "control characters"},
		{"nope", "1", "Unknown setting"},
	}
	for _, c := range bad {
		if _, err := Clean(c.key, json.RawMessage(c.raw)); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s=%s: want %q, got %v", c.key, c.raw, c.msg, err)
		}
	}
}

func TestEveryDefHasValidDefault(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range Groups {
		groups[g.Key] = true
	}
	for _, d := range Defs {
		raw, _ := json.Marshal(d.Default)
		if _, err := Clean(d.Key, raw); err != nil {
			t.Errorf("%s default rejected: %v", d.Key, err)
		}
		if !groups[d.Group] {
			t.Errorf("%s has unknown group %q", d.Key, d.Group)
		}
	}
}

func TestCleanNetwork(t *testing.T) {
	n, err := Network{Bind: " 0.0.0.0 ", Port: 443, TLS: TLSCustom,
		TrustedProxies: []string{" 192.0.2.10 ", "", "172.16.0.0/12", "::1"}, BasePath: " backups/ "}.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if n.Bind != "" || n.BasePath != "/backups" || len(n.TrustedProxies) != 3 || n.ListenAddr() != ":443" {
		t.Fatalf("got %+v", n)
	}
	if n, _ := (Network{Bind: "[::1]", Port: 8099, TLS: TLSOff}).Clean(); n.Bind != "::1" || n.ListenAddr() != "[::1]:8099" {
		t.Fatalf("ipv6 bind: %+v", n)
	}
	bad := []Network{
		{Bind: "nas.lan", Port: 8099, TLS: TLSOff},
		{Port: 0, TLS: TLSOff},
		{Port: 70000, TLS: TLSOff},
		{Port: 8099, TLS: "maybe"},
		{Port: 8099, TLS: TLSOff, TrustedProxies: []string{"proxy.lan"}},
		{Port: 8099, TLS: TLSOff, BasePath: "/a/../b"},
		{Port: 8099, TLS: TLSOff, BasePath: "/back ups"},
		{Port: 8099, TLS: TLSOff, BasePath: "/api"},
	}
	for _, b := range bad {
		if _, err := b.Clean(); err == nil {
			t.Errorf("%+v should be rejected", b)
		}
	}
	if bp, _ := CleanBasePath("/"); bp != "" {
		t.Error("/ means no base path")
	}
}

func TestNeedsConfirm(t *testing.T) {
	a := DefaultNetwork()
	b := a
	b.TrustedProxies = []string{"192.0.2.10"}
	if a.NeedsConfirm(b) {
		t.Error("changing only trusted proxies can't lock you out")
	}
	for _, change := range []func(*Network){
		func(n *Network) { n.Port = 9000 },
		func(n *Network) { n.Bind = "127.0.0.1" },
		func(n *Network) { n.TLS = TLSOff },
		func(n *Network) { n.BasePath = "/x" },
	} {
		c := a
		change(&c)
		if !a.NeedsConfirm(c) {
			t.Errorf("%+v should need confirming", c)
		}
	}
}
