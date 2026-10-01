package config

import (
	"net"
	"strconv"
	"strings"
)

// TLS modes.
const (
	TLSSelfSigned = "self-signed"
	TLSCustom     = "custom"
	TLSOff        = "off"
)

// Network is how the web UI is reached.
type Network struct {
	// Bind is an IP address, or "" for every interface.
	Bind           string   `json:"bind"`
	Port           int      `json:"port"`
	TLS            string   `json:"tls"`
	TrustedProxies []string `json:"trusted_proxies"`
	// BasePath is "" or a path like "/backups" (no trailing slash).
	BasePath string `json:"base_path"`
}

// DefaultNetwork is a fresh install's setting: every interface, port 8099,
// HTTPS with a self-signed certificate.
func DefaultNetwork() Network {
	return Network{Bind: "", Port: 8099, TLS: TLSSelfSigned, TrustedProxies: []string{}, BasePath: ""}
}

// AllInterfaces reports whether bind means "every interface".
func AllInterfaces(bind string) bool {
	return bind == "" || bind == "0.0.0.0" || bind == "::" || bind == "[::]"
}

// Clean checks and normalises a network setting.
func (n Network) Clean() (Network, error) {
	out := n
	out.Bind = strings.Trim(strings.TrimSpace(n.Bind), "[]")
	if AllInterfaces(out.Bind) {
		out.Bind = ""
	} else if ip := net.ParseIP(out.Bind); ip == nil {
		return n, invalid("Listen address must be an IP address of this machine, or blank for every interface.")
	} else {
		out.Bind = ip.String()
	}
	if n.Port < 1 || n.Port > 65535 {
		return n, invalid("Port must be between 1 and 65535.")
	}
	switch n.TLS {
	case TLSSelfSigned, TLSCustom, TLSOff:
	default:
		return n, invalid("Choose how HTTPS should work.")
	}
	out.TrustedProxies = []string{}
	for _, item := range n.TrustedProxies {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(item); err != nil && net.ParseIP(item) == nil {
			return n, invalid("“%s” isn't an IP address or network (like 192.0.2.10 or 172.16.0.0/12).", item)
		}
		out.TrustedProxies = append(out.TrustedProxies, item)
	}
	if len(out.TrustedProxies) > 32 {
		return n, invalid("List at most 32 trusted proxies.")
	}
	bp, err := CleanBasePath(n.BasePath)
	if err != nil {
		return n, err
	}
	out.BasePath = bp
	return out, nil
}

// CleanBasePath turns " backups/ " into "/backups" and "/" into "".
func CleanBasePath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", invalid("The base path can't contain empty, “.” or “..” parts.")
		}
		for _, r := range seg {
			ok := r == '-' || r == '_' || r == '.' || r == '~' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return "", invalid("The base path can use letters, numbers, dashes, dots and slashes, like /backups.")
			}
		}
	}
	if p == "api" || strings.HasPrefix(p, "api/") {
		return "", invalid("The base path can't start with /api.")
	}
	return "/" + p, nil
}

// ListenAddr is the address to listen on.
func (n Network) ListenAddr() string {
	if n.Bind == "" {
		return net.JoinHostPort("", strconv.Itoa(n.Port))
	}
	return net.JoinHostPort(n.Bind, strconv.Itoa(n.Port))
}

// NeedsConfirm reports whether moving from n to next could lock the user
// out, so it must be confirmed from the new address before it's kept.
func (n Network) NeedsConfirm(next Network) bool {
	return n.Bind != next.Bind || n.Port != next.Port || n.TLS != next.TLS || n.BasePath != next.BasePath
}

// UsesTLS reports whether the server speaks HTTPS itself.
func (n Network) UsesTLS() bool { return n.TLS != TLSOff }
