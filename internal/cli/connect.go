package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// `dock connect` (ADR 0023): a port on 127.0.0.1 whose every connection
// is tunnelled through the API to the service's private port, one upgraded
// HTTP request per connection.

// connectOutput is `dock connect --json`'s one line before it serves.
type connectOutput struct {
	Service  string `json:"service"`
	Port     int    `json:"port"`
	Local    string `json:"local"`
	Engine   string `json:"engine,omitempty"`
	User     string `json:"user,omitempty"`
	Database string `json:"database,omitempty"`
	URL      string `json:"url,omitempty"`
}

func newConnectCmd(g *globals) *cobra.Command {
	var (
		f       targetFlags
		port    int
		confirm string
		showURL bool
	)
	cmd := &cobra.Command{
		Use:   "connect [service]",
		Short: "Tunnel a local port to a service's private port (psql, TablePlus, redis-cli)",
		Long: `Listen on 127.0.0.1 and carry every connection to the service's port on
its environment's private network, through the API. The service never gets
a public port. Runs until Ctrl-C.

A tunnel is raw access (no masking, no changesets), so it needs data access
level write with sensitive visible (admins have it; ` + "`dock db status`" + ` shows
yours), and production needs --confirm <environment>. Each connection is in
the audit log (data.connect, data.disconnect), never its traffic.

The local port is the service's (5432 for Postgres) when it is free, else
any free one; --port picks it. --show-url prints the connection URL with its
password, pointed at the local port (audited as data.reveal).`,
		Example: `  dock connect db                  # then: psql -h 127.0.0.1 -p 5432 -U postgres app
  dock connect db --show-url       # prints postgres://postgres:...@127.0.0.1:5432/app
  dock connect cache --port 6380 -e production --confirm production`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			q := url.Values{}
			if confirm != "" {
				q.Set("confirm", confirm)
			}
			infoQuery := maps.Clone(q)
			if showURL {
				infoQuery.Set("reveal", "true")
			}
			info, err := get[apitypes.ConnectInfo](ctx, s, t.servicePath()+"/connect?"+infoQuery.Encode())
			if err != nil {
				return connectHint(err)
			}
			ln, err := listenLocal(port, info.Port)
			if err != nil {
				return err
			}
			defer func() { _ = ln.Close() }()
			local := ln.Addr().String()

			out := connectOutput{Service: info.Service, Port: info.Port, Local: local, Engine: string(info.Engine), User: info.User, Database: info.Database}
			if info.URL != "" {
				out.URL = localURL(info.URL, local)
			}
			if err := g.io.Print(out, func() error {
				g.io.Successf("%s port %d on %s", info.Service, info.Port, local)
				if err := g.io.Fields("url", out.URL); err != nil {
					return err
				}
				if out.URL == "" && info.Engine == apitypes.EnginePostgres {
					g.io.Detail(fmt.Sprintf(`psql "postgres://%s@%s/%s" (the password: --show-url)`, url.PathEscape(info.User), local, url.PathEscape(info.Database)))
				}
				g.io.Detail("Every connection is audited. Ctrl-C to stop.")
				return nil
			}); err != nil {
				return err
			}
			path := t.servicePath() + "/tunnel"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			return serveTunnels(ctx, ln, func(ctx context.Context) (io.ReadWriteCloser, error) {
				return s.api.Upgrade(ctx, path, apitypes.TunnelProtocol)
			}, func(err error) { g.io.PrintError(connectHint(err)) })
		},
	}
	f.add(cmd, needService)
	cmd.Flags().IntVar(&port, "port", 0, "local port (default: the service's port if free, else any free one)")
	cmd.Flags().StringVar(&confirm, "confirm", "", "the environment's name; production needs it")
	cmd.Flags().BoolVar(&showURL, "show-url", false, "print the connection URL with its password (audited)")
	return cmd
}

// connectHint replaces the generic hint of a permission error.
func connectHint(err error) error {
	e, ok := errors.AsType[*output.Error](err)
	if p, isProblem := errors.AsType[*apiclient.Problem](err); ok && isProblem && p.Status == http.StatusForbidden {
		e.Hint = "`dock db status` shows your data access and why; tunnels need level write with sensitive visible. An admin changes it with `dock db policy set` or on your token."
	}
	return err
}

// listenLocal listens on 127.0.0.1: on port if set, else on the service's
// port, else on any free one.
func listenLocal(port, servicePort int) (net.Listener, error) {
	if port != 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return nil, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("can't listen on 127.0.0.1:%d", port), Hint: "Pick another --port, or leave it out for a free one.", Err: err}
		}
		return ln, nil
	}
	if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(servicePort))); err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// localURL points a connection URL at the local listener, keeping its
// scheme, credentials, path and query.
func localURL(raw, local string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.Host = local
	return u.String()
}

// serveTunnels accepts connections on ln until ctx ends, opening a tunnel
// for each and copying both ways until either side closes. A tunnel that
// can't open is reported to fail and its connection closed; the others go
// on. When ctx ends, every connection is closed and serveTunnels returns
// nil once they are.
func serveTunnels(ctx context.Context, ln net.Listener, open func(context.Context) (io.ReadWriteCloser, error), fail func(error)) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		local, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() {
			remote, err := open(ctx)
			if err != nil {
				_ = local.Close()
				if ctx.Err() == nil {
					fail(err)
				}
				return
			}
			pipe(ctx, local, remote)
		})
	}
}

// pipe copies between a local connection and its tunnel until either
// side ends or ctx does, then closes both.
func pipe(ctx context.Context, local net.Conn, remote io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _, _ = local.Close(), remote.Close() }) }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(remote, local)
		closeBoth()
	}()
	_, _ = io.Copy(local, remote)
	closeBoth()
	<-done
}
