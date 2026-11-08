package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Log drains (ADR 0046). A drain sends an environment's app output to an
// HTTP endpoint (JSON lines), a syslog server or Loki. Admins of the
// project add them; the URL, headers and token are sealed like alert
// channels. While an environment has a drain, the control plane follows
// each of its running service containers and queues every line for each
// drain (drains_deliver.go); a sender per drain posts them in batches
// (drains_sink.go). Nothing runs for an environment without one.

// drainConfig is a drain's sealed settings.
type drainConfig struct {
	// URL is where lines go: the http endpoint, Loki's push URL, or
	// tcp://, tls:// or udp:// host:port for syslog.
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Token   string            `json:"token,omitempty"`
}

// maxDrainHeaders bounds the headers a drain sends.
const maxDrainHeaders = 16

// headerNameRe is an HTTP header name (RFC 9110 token).
var headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,128}$")

// ListLogDrains lists an environment's drains with how their delivery
// goes, without their secrets. Scope: read.
func (c *Core) ListLogDrains(ctx context.Context, a Actor, ref EnvRef) ([]apitypes.LogDrain, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "listing log drains")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListLogDrains(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.LogDrain, len(rows))
	for i, r := range rows {
		out[i] = c.toLogDrain(r)
	}
	return out, nil
}

// AddLogDrain adds a drain to an environment and starts sending its
// services' output there. Scope: admin.
func (c *Core) AddLogDrain(ctx context.Context, a Actor, ref EnvRef, req apitypes.AddLogDrainRequest) (apitypes.LogDrain, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "adding log drains")
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	name := req.Name
	if name == "" {
		name = string(req.Kind)
	}
	if err := validName("log drain", name); err != nil {
		return apitypes.LogDrain{}, err
	}
	cfg, target, err := c.logDrains.settings(req)
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	sealed, err := c.box.Seal(plain, logDrainAAD(es.env.ID, name))
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	row, err := c.store.AddLogDrain(ctx, store.AddLogDrainParams{
		EnvironmentID: es.env.ID, Kind: string(req.Kind), Name: name, Target: target, Config: sealed,
		CreatedBy: a.Name, CreatedAt: now(),
	})
	if store.IsUnique(err) {
		return apitypes.LogDrain{}, conflict("%s already has a log drain named %q; pass --name to name this one, or remove that one first", es, name)
	}
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	if c.addLogDrain(row, cfg) {
		c.followEnv(c.ctx, es.env.ID, 0)
	}
	c.audit(ctx, a, "log_drain.add", &es.project, &es.env, "log drain "+name,
		fmt.Sprintf("added %s log drain %s to %s, sending %s's output", req.Kind, name, target, es))
	return c.toLogDrain(row), nil
}

// RemoveLogDrain deletes a drain; what it had queued is dropped. With the
// environment's last drain gone, its containers are no longer followed.
// Scope: admin.
func (c *Core) RemoveLogDrain(ctx context.Context, a Actor, ref EnvRef, id int64) error {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "removing log drains")
	if err != nil {
		return err
	}
	row, err := c.logDrain(ctx, es, id)
	if err != nil {
		return err
	}
	if err := c.store.DeleteLogDrain(ctx, row.ID); err != nil {
		return err
	}
	c.removeLogDrain(row.ID)
	c.audit(ctx, a, "log_drain.remove", &es.project, &es.env, "log drain "+row.Name,
		fmt.Sprintf("removed %s log drain %s (%s) from %s", row.Kind, row.Name, row.Target, es))
	return nil
}

// TestLogDrain sends one test line to a drain, once and without retries,
// and says how it went; the outcome shows in the drain's status like any
// delivery. Scope: admin.
func (c *Core) TestLogDrain(ctx context.Context, a Actor, ref EnvRef, id int64) (apitypes.LogDrainTest, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "testing log drains")
	if err != nil {
		return apitypes.LogDrainTest{}, err
	}
	row, err := c.logDrain(ctx, es, id)
	if err != nil {
		return apitypes.LogDrainTest{}, err
	}
	d := c.logDrains.get(row.ID)
	if d == nil {
		return apitypes.LogDrainTest{}, notFound("log drain %d isn't running; the daemon may be shutting down", id)
	}
	src := newLogSource(es.project.Name, es.env.Name, "dock", 0)
	line := drainRecord{Time: now(), Stream: "stdout", Text: "Test line from dock: log drain " + row.Name + " works", src: src}
	out := apitypes.LogDrainTest{OK: true}
	if err := c.logDrains.deliver(ctx, d, []drainRecord{line}, nil); err != nil {
		out.OK, out.Error = false, err.Error()
		d.failed(err)
	} else {
		d.delivered(1)
	}
	out.Drain = c.toLogDrain(row)
	return out, nil
}

// logDrain loads an environment's drain by ID.
func (c *Core) logDrain(ctx context.Context, es envScope, id int64) (store.LogDrain, error) {
	row, err := c.store.GetLogDrain(ctx, store.GetLogDrainParams{ID: id, EnvironmentID: es.env.ID})
	if store.IsNotFound(err) {
		return row, notFound("log drain %d not found in %s; `dock logs drain list` lists them", id, es)
	}
	return row, err
}

// toLogDrain is a drain for the API, with its in-memory status.
func (c *Core) toLogDrain(r store.LogDrain) apitypes.LogDrain {
	out := apitypes.LogDrain{
		ID: r.ID, Kind: apitypes.LogDrainKind(r.Kind), Name: r.Name, Target: r.Target,
		CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy,
	}
	if d := c.logDrains.get(r.ID); d != nil {
		out.Status = d.status()
	}
	return out
}

// settings validates a new drain's settings and returns what is sealed
// and what lists show. Literal addresses the guard refuses are refused
// here; names are checked on every connection, after DNS.
func (m *drainManager) settings(req apitypes.AddLogDrainRequest) (drainConfig, string, error) {
	var cfg drainConfig
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" {
		return cfg, "", invalid("%q is not a URL; %s", req.URL, drainURLHint(req.Kind))
	}
	switch req.Kind {
	case apitypes.DrainHTTP, apitypes.DrainLoki:
		if u.Scheme != "http" && u.Scheme != "https" {
			return cfg, "", invalid("a %s log drain needs an http:// or https:// URL", req.Kind)
		}
		if len(req.Headers) > maxDrainHeaders {
			return cfg, "", invalid("a log drain takes at most %d headers", maxDrainHeaders)
		}
		for k, v := range req.Headers {
			if !headerNameRe.MatchString(k) || strings.ContainsAny(v, "\r\n") {
				return cfg, "", invalid("header %q is not a valid HTTP header; pass --header Name=value", k)
			}
			switch http.CanonicalHeaderKey(k) {
			case "Host", "Content-Type", "Content-Length", "Content-Encoding", "Transfer-Encoding":
				return cfg, "", invalid("the %s header is set by dock; leave it out", k)
			}
		}
		if strings.ContainsAny(req.Token, "\r\n") {
			return cfg, "", invalid("the token must be one line")
		}
		cfg.URL, cfg.Headers, cfg.Token = req.URL, req.Headers, req.Token
		if req.Kind == apitypes.DrainLoki && !strings.HasSuffix(u.Path, lokiPushPath) {
			pu := *u
			pu.Path = strings.TrimSuffix(u.Path, "/") + lokiPushPath
			cfg.URL = pu.String()
		}
	case apitypes.DrainSyslog:
		port, ok := syslogPorts[u.Scheme]
		if !ok {
			return cfg, "", invalid("a syslog log drain needs a tcp://, tls:// or udp:// URL")
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return cfg, "", invalid("a syslog URL is just scheme://host:port, like tcp://logs.example.com:514")
		}
		if len(req.Headers) > 0 || req.Token != "" {
			return cfg, "", invalid("syslog log drains take no headers or token")
		}
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(port))
		}
		cfg.URL = u.Scheme + "://" + u.Host
	default:
		return cfg, "", invalid("log drain kind must be http, syslog or loki")
	}
	if err := m.guard.hostErr(u.Hostname()); err != nil {
		return cfg, "", invalid("%v", err)
	}
	return cfg, u.Scheme + "://" + u.Host, nil
}

// syslogPorts are the syslog schemes and their default ports.
var syslogPorts = map[string]int{"tcp": 514, "udp": 514, "tls": 6514}

// drainURLHint says what URL a kind of drain takes.
func drainURLHint(kind apitypes.LogDrainKind) string {
	if kind == apitypes.DrainSyslog {
		return "syslog takes tcp://, tls:// or udp:// host:port"
	}
	return "pass an http:// or https:// URL"
}

// openLogDrain opens a drain's sealed settings.
func (c *Core) openLogDrain(r store.LogDrain) (drainConfig, error) {
	var cfg drainConfig
	plain, err := c.box.Open(r.Config, logDrainAAD(r.EnvironmentID, r.Name))
	if err != nil {
		return cfg, fmt.Errorf("open log drain settings: %w", err)
	}
	return cfg, json.Unmarshal(plain, &cfg)
}

// logDrainAAD binds a drain's sealed settings to its environment and name.
func logDrainAAD(envID int64, name string) secrets.AAD {
	return sealedLogDrain.AAD(strconv.FormatInt(envID, 10), name)
}

// startLogDrains loads every drain and follows the containers on this
// server, for errors and drains alike (drains_deliver.go). Start calls
// it; containers on remote servers are followed as each server connects
// (followServer).
func (c *Core) startLogDrains(ctx context.Context) error {
	rows, err := c.store.ListAllLogDrains(ctx)
	if err != nil {
		return fmt.Errorf("load log drains: %w", err)
	}
	for _, r := range rows {
		cfg, err := c.openLogDrain(r)
		if err != nil {
			c.log.Error("log drain settings", "drain", r.ID, "err", err)
			continue
		}
		c.addLogDrain(r, cfg)
	}
	c.followServer(ctx, c.localID)
	return nil
}

// drainRetryMin and drainRetryMax bound a failing drain's backoff; see
// deliverBatch.
const (
	drainRetryMin = time.Second
	drainRetryMax = time.Minute
)
