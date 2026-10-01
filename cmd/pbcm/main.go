// Command pbcm is the PBC Manager server.
//
//	pbcm serve          run the web UI
//	pbcm setup-code     show the one-time setup code
//	pbcm passwd         set the admin password
//	pbcm totp-reset     turn off two-step verification
//	pbcm network        change or reset how the web UI is reached
//	pbcm version        print the version
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/bradyloveland/pbcmanager/internal/auth"
	"github.com/bradyloveland/pbcmanager/internal/config"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/server"
	"github.com/bradyloveland/pbcmanager/internal/store"
	"github.com/bradyloveland/pbcmanager/internal/version"
)

type dirs struct{ config, data string }

func (d *dirs) flags(fs *flag.FlagSet) {
	fs.StringVar(&d.config, "config-dir", envOr("PBCM_CONFIG_DIR", "/etc/pbcm"), "settings folder: secret key and certificates")
	fs.StringVar(&d.data, "data-dir", envOr("PBCM_DATA_DIR", "/var/lib/pbcm"), "data folder: the database")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (d dirs) open() (*store.Store, error) {
	for _, dir := range []string{d.config, d.data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("can't create %s: %w", dir, err)
		}
	}
	box, err := secret.LoadOrCreate(filepath.Join(d.config, "secret.key"))
	if err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(d.data, "pbcm.db"), box)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "setup-code":
		err = cmdSetupCode(args)
	case "passwd":
		err = cmdPasswd(args)
	case "totp-reset":
		err = cmdTOTPReset(args)
	case "network":
		err = cmdNetwork(args)
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q.\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `%s %s

Usage: pbcm <command> [options]

Commands:
  serve        Run the web UI
  setup-code   Show the one-time code for finishing setup in the browser
  passwd       Set the admin password (and optionally the username)
  totp-reset   Turn off two-step verification if you've lost your authenticator
  network      Change how the web UI is reached, or --reset to the defaults
  version      Print the version

Every command takes --config-dir and --data-dir (defaults /etc/pbcm and
/var/lib/pbcm). Run "pbcm <command> -h" for its options.
`, version.Name, version.Version)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var d dirs
	d.flags(fs)
	debug := fs.Bool("debug", false, "log every request")
	_ = fs.Parse(args)

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	st, err := d.open()
	if err != nil {
		return err
	}
	defer st.Close()
	srv, err := server.New(server.Options{ConfigDir: d.config, DataDir: d.data, Store: st, RunnerPath: os.Getenv("PBCM_RUNNER")})
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}
	slog.Info("started", "version", version.Version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func cmdSetupCode(args []string) error {
	fs := flag.NewFlagSet("setup-code", flag.ExitOnError)
	var d dirs
	d.flags(fs)
	_ = fs.Parse(args)
	st, err := d.open()
	if err != nil {
		return err
	}
	defer st.Close()
	code, err := server.EnsureSetupCode(d.config, st)
	if err != nil {
		return err
	}
	if code == "" {
		fmt.Println("Setup is already done. Sign in with your admin username and password.")
		return nil
	}
	fmt.Println(code)
	return nil
}

func readPassword(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "New admin password: ")
	a, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errors.New("can't read a password here; use --password-stdin")
	}
	fmt.Fprint(os.Stderr, "Repeat it: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("the passwords don't match")
	}
	return string(a), nil
}

func cmdPasswd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	var d dirs
	d.flags(fs)
	username := fs.String("username", "", "also change the username (required if setup isn't done yet)")
	stdin := fs.Bool("password-stdin", false, "read the password from standard input")
	_ = fs.Parse(args)
	st, err := d.open()
	if err != nil {
		return err
	}
	defer st.Close()
	pw, err := readPassword(*stdin)
	if err != nil {
		return err
	}
	if len(pw) < 10 {
		return errors.New("use at least 10 characters")
	}
	hash, err := auth.HashPassword(pw, auth.DefaultIterations)
	if err != nil {
		return err
	}
	has, err := st.HasAdmin()
	if err != nil {
		return err
	}
	if !has {
		name := *username
		if name == "" {
			name = "admin"
		}
		if err := st.CreateAdmin(name, hash); err != nil {
			return err
		}
		os.Remove(server.SetupCodePath(d.config))
		fmt.Printf("Admin account %q created. Sign in from the browser.\n", name)
		return nil
	}
	a, err := st.UpdateAdmin(func(a *store.Admin) error {
		a.PasswordHash = hash
		if *username != "" {
			a.Username = *username
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := st.DeleteOtherSessions(""); err != nil {
		return err
	}
	fmt.Printf("Password changed for %q. Every browser has been signed out.\n", a.Username)
	if a.TOTPSecret != "" {
		fmt.Println("Two-step verification is still on. If you've also lost your authenticator, run: sudo pbcm totp-reset")
	}
	return nil
}

func cmdTOTPReset(args []string) error {
	fs := flag.NewFlagSet("totp-reset", flag.ExitOnError)
	var d dirs
	d.flags(fs)
	_ = fs.Parse(args)
	st, err := d.open()
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.UpdateAdmin(func(a *store.Admin) error {
		a.TOTPSecret, a.TOTPLastStep, a.RecoveryCodes = "", 0, []string{}
		return nil
	}); errors.Is(err, store.ErrNoAdmin) {
		return errors.New("setup isn't done yet, so there's nothing to reset")
	} else if err != nil {
		return err
	}
	fmt.Println("Two-step verification is off. Sign in with your password and set it up again under Account.")
	return nil
}

func cmdNetwork(args []string) error {
	fs := flag.NewFlagSet("network", flag.ExitOnError)
	var d dirs
	d.flags(fs)
	reset := fs.Bool("reset", false, "go back to the defaults: every interface, port 8099, self-signed HTTPS, no proxy settings")
	bind := fs.String("bind", "\x00", "listen address (\"\" for every interface)")
	port := fs.Int("port", 0, "port")
	tlsMode := fs.String("tls", "", "self-signed, custom or off")
	proxies := fs.String("trusted-proxies", "\x00", "comma-separated proxy addresses or networks (\"\" for none)")
	base := fs.String("base-path", "\x00", "sub-path such as /backups (\"\" for none)")
	show := fs.Bool("show", false, "print \"<scheme> <port> <bind|-> <base path|->\" and change nothing")
	_ = fs.Parse(args)
	st, err := d.open()
	if err != nil {
		return err
	}
	defer st.Close()
	n := config.DefaultNetwork()
	if !*reset {
		if _, err := st.GetSetting("network", &n); err != nil {
			return err
		}
	}
	changes := 0
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "show" && f.Name != "config-dir" && f.Name != "data-dir" {
			changes++
		}
	})
	if *show || changes == 0 {
		scheme, bindOut, baseOut := "https", n.Bind, n.BasePath
		if n.TLS == config.TLSOff {
			scheme = "http"
		}
		if bindOut == "" {
			bindOut = "-"
		}
		if baseOut == "" {
			baseOut = "-"
		}
		fmt.Println(scheme, n.Port, bindOut, baseOut)
		return nil
	}
	if *bind != "\x00" {
		n.Bind = *bind
	}
	if *port != 0 {
		n.Port = *port
	}
	if *tlsMode != "" {
		n.TLS = *tlsMode
	}
	if *proxies != "\x00" {
		n.TrustedProxies = strings.FieldsFunc(*proxies, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if *base != "\x00" {
		n.BasePath = *base
	}
	if n.TrustedProxies == nil {
		n.TrustedProxies = []string{}
	}
	clean, err := n.Clean()
	if err != nil {
		return err
	}
	if clean.TLS == config.TLSCustom {
		if _, err := os.Stat(filepath.Join(d.config, "tls", "custom.crt")); err != nil {
			return errors.New("there's no custom certificate yet; upload one from Settings in the web UI first")
		}
	}
	if err := st.SetSetting("network", clean); err != nil {
		return err
	}
	where := "every interface"
	if clean.Bind != "" {
		where = clean.Bind
	}
	basePath := "none"
	if clean.BasePath != "" {
		basePath = clean.BasePath + "/"
	}
	proxyList := "none"
	if len(clean.TrustedProxies) > 0 {
		proxyList = strings.Join(clean.TrustedProxies, ", ")
	}
	fmt.Printf("Listen on %s, port %d, HTTPS %s, base path %s, trusted proxies %s.\n",
		where, clean.Port, clean.TLS, basePath, proxyList)
	fmt.Println("Restart the service to apply: sudo systemctl restart pbcm")
	return nil
}
