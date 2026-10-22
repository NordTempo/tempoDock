// Command tempobase is the standalone tempoBase server (ADR 0024): a
// Postgres viewer with safe editing, built from Tempo Dock's data panel.
// It uses the standard flag package, so nothing runs at start-up that a
// subcommand doesn't need.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbhttp"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
	"github.com/NordTempo/tempoDock/internal/version"
)

const usage = `tempobase: a self-hosted Postgres viewer with safe editing

Usage:
  tempobase serve [--config FILE]
  tempobase admin create-user --email E --name N --role viewer|editor|admin [--link] [--config FILE]
  tempobase admin reset-link --email E [--config FILE]
  tempobase admin backup FILE [--config FILE]
  tempobase openapi [-o FILE]
  tempobase healthcheck [--url URL]
  tempobase version

Configuration: the TOML file (--config, else $TEMPOBASE_CONFIG), then
TEMPOBASE_* environment variables, which win.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempobase:", err)
		os.Exit(1)
	}
}

// errUsage is returned for a wrong command line; usage was printed.
var errUsage = errors.New("see the usage above")

// run runs the command in args.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return errUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "serve":
		return serve(ctx, args, stderr)
	case "admin":
		return admin(ctx, args, stdin, stdout, stderr)
	case "openapi":
		return openapi(args, stdout, stderr)
	case "healthcheck":
		return healthcheck(ctx, args, stderr)
	case "version", "--version":
		_, err := fmt.Fprintln(stdout, "tempobase", version.Version)
		return err
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage)
		return err
	}
	_, _ = io.WriteString(stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

// flags returns a flag set for a subcommand that prints to stderr.
func flags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("tempobase "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// serve runs the server until ctx is done.
func serve(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flags("serve", stderr)
	configPath := fs.String("config", "", "TOML config file (default $TEMPOBASE_CONFIG, else none)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := tempobase.LoadConfig(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	app, err := tempobase.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: tbhttp.New(app, tbhttp.Options{Logger: log}), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("tempobase listening", "version", version.Version, "listen", cfg.Listen, "data", cfg.DataDir)
	code, ok, err := app.StartSetup(ctx)
	if err != nil {
		_ = srv.Close()
		return err
	}
	if ok {
		// The fragment keeps the code out of proxy logs.
		log.Info("no users yet: open " + baseURL(cfg) + "/setup#code=" + code + " to create the first admin")
	}

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if serr := srv.Shutdown(shutdownCtx); serr != nil && err == nil {
		err = serr
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// admin runs `tempobase admin ...` on the database directly; it works
// while the server runs (WAL).
func admin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return errUsage
	}
	sub, args := args[0], args[1:]
	fs := flags("admin "+sub, stderr)
	configPath := fs.String("config", "", "TOML config file (default $TEMPOBASE_CONFIG, else none)")
	var email, name, role *string
	var link *bool
	switch sub {
	case "create-user":
		email = fs.String("email", "", "the person's email (required)")
		name = fs.String("name", "", "the person's name (required)")
		role = fs.String("role", "", "viewer, editor or admin (required)")
		link = fs.Bool("link", false, "print a set-password link instead of reading a password from stdin")
	case "reset-link":
		email = fs.String("email", "", "the person's email (required)")
	case "backup":
	default:
		_, _ = io.WriteString(stderr, usage)
		return fmt.Errorf("unknown admin command %q", sub)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if sub == "backup" && fs.NArg() != 1 || sub != "backup" && fs.NArg() != 0 {
		fs.Usage()
		return errUsage
	}
	cfg, err := tempobase.LoadConfig(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	app, err := tempobase.Open(ctx, cfg, slog.New(slog.NewTextHandler(stderr, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	switch sub {
	case "create-user":
		password := ""
		if !*link {
			if password, err = readPassword(stdin); err != nil {
				return err
			}
		}
		u, l, err := app.CreateUser(ctx, tempobase.System, *email, *name, tbtypes.Role(*role), password)
		if err != nil {
			return err
		}
		if l == nil {
			_, err = fmt.Fprintf(stdout, "created %s %s\n", u.Role, u.Email)
			return err
		}
		_, err = fmt.Fprintf(stdout, "created %s %s; send them this link to set a password (valid until %s):\n%s\n",
			u.Role, u.Email, l.ExpiresAt.Format(time.RFC3339), resolve(cfg, l.URL))
		return err
	case "reset-link":
		l, err := app.ResetLinkByEmail(ctx, tempobase.System, *email, "")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "send %s this link to set a new password (valid until %s):\n%s\n",
			l.Email, l.ExpiresAt.Format(time.RFC3339), resolve(cfg, l.URL))
		return err
	}
	path := fs.Arg(0)
	if err := app.Store().Backup(ctx, path); err != nil {
		return fmt.Errorf("backup to %s: %w", path, err)
	}
	_, err = fmt.Fprintf(stdout, "wrote %s; keep it with the key file %s, which it needs\n", path, cfg.SecretsKeyFile)
	return err
}

// readPassword reads a password from the first line of stdin.
func readPassword(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password from stdin: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("pipe a password on stdin, or use --link to print a set-password link")
	}
	return password, nil
}

// resolve completes a link made without a base (the admin commands have
// no request to take it from).
func resolve(cfg tempobase.Config, link string) string {
	if strings.HasPrefix(link, "/") {
		return baseURL(cfg) + link
	}
	return link
}

// baseURL is where people reach the server: base_url, else the listen
// address (loopback when it listens on every interface).
func baseURL(cfg tempobase.Config) string {
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	return "http://" + localAddr(cfg.Listen)
}

// localAddr is a listen address as this machine reaches it.
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// openapi writes the API's OpenAPI document (web/tempobase/openapi.json,
// `go generate ./internal/tempobase/tbhttp`).
func openapi(args []string, stdout, stderr io.Writer) error {
	fs := flags("openapi", stderr)
	out := fs.String("o", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec, err := tbhttp.Spec()
	if err != nil {
		return err
	}
	spec = append(spec, '\n')
	if *out == "" {
		_, err = stdout.Write(spec)
		return err
	}
	return os.WriteFile(*out, spec, 0o644)
}

// healthcheck exits 0 when the server's health endpoint answers, for a
// container health check that someone opts into.
func healthcheck(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flags("healthcheck", stderr)
	listen := os.Getenv("TEMPOBASE_LISTEN")
	if listen == "" {
		listen = tempobase.DefaultListen
	}
	url := fs.String("url", "http://"+localAddr(listen)+tbhttp.Prefix+"/health", "the health endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", *url, res.Status)
	}
	return nil
}
