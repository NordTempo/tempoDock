package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/smtp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Notification channels (ADR 0020). Admins add channels: a generic webhook
// (JSON, HMAC-signed), a Slack-compatible incoming webhook, or email over
// SMTP. A channel gets alerts at or above its minimum severity when they
// open, escalate and resolve, and a reminder every renotify_minutes while
// one stays open. Each channel's notifications are sent in order by a
// goroutine that runs while it has some queued, each retried with backoff
// (deliveryBackoff); the outcome is kept on the channel, so failures show
// in `dock alerts channel list` and the UI. Queues live in memory only: a
// restart drops what wasn't sent.

// deliveryBackoff is how long each delivery attempt waits before it runs:
// the first right away, the last about 20 minutes after the alert.
var deliveryBackoff = []time.Duration{0, 10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

const (
	// maxQueued caps the notifications waiting for one channel; more are
	// dropped and counted as failures.
	maxQueued = 32
	// deliveryTimeout bounds one attempt.
	deliveryTimeout = 15 * time.Second
	// defaultRenotify is how often an open alert is sent again unless the
	// channel says otherwise.
	defaultRenotify = 240
	// SignatureHeader carries a webhook delivery's signature:
	// t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">.
	SignatureHeader = "Dock-Signature"
)

// channelConfig is a channel's sealed settings.
type channelConfig struct {
	URL    string                 `json:"url,omitempty"`
	Secret string                 `json:"secret,omitempty"`
	SMTP   *apitypes.SMTPSettings `json:"smtp,omitempty"`
}

// notify sends what happened to an alert (opened, escalated, resolved) to
// the channels that should hear it: for opened and escalated those whose
// minimum severity it meets, for resolved those that were told it opened.
func (c *Core) notify(ctx context.Context, a store.Alert, event string, now time.Time) {
	channels, err := c.store.ListAlertChannels(ctx)
	if err != nil || len(channels) == 0 {
		return
	}
	var to []store.AlertChannel
	if event == "resolved" {
		told, err := c.store.ListAlertNotices(ctx, a.ID)
		if err != nil {
			c.log.Error("alerts: list notices", "err", err)
			return
		}
		for _, ch := range channels {
			if slices.Contains(told, ch.ID) {
				to = append(to, ch)
			}
		}
		if err := c.store.DeleteAlertNotices(ctx, a.ID); err != nil {
			c.log.Error("alerts: delete notices", "err", err)
		}
	} else {
		for _, ch := range channels {
			if severityRank(apitypes.AlertSeverity(a.Severity)) < severityRank(apitypes.AlertSeverity(ch.MinSeverity)) {
				continue
			}
			if err := c.store.AddAlertNotice(ctx, store.AddAlertNoticeParams{AlertID: a.ID, ChannelID: ch.ID, NotifiedAt: now.Unix()}); err != nil {
				c.log.Error("alerts: record notice", "err", err)
			}
			to = append(to, ch)
		}
	}
	n := apitypes.AlertNotification{Event: event, Alert: toAlert(a), SentAt: now.UTC()}
	for _, ch := range to {
		c.deliver(ch, n)
	}
}

// remind sends open alerts again to channels whose renotify interval has
// passed since they were last told. With no open alert it does nothing,
// not even a query.
func (c *Core) remind(ctx context.Context, now time.Time) {
	c.alerts.mu.Lock()
	byID := make(map[int64]store.Alert, len(c.alerts.open))
	for _, a := range c.alerts.open {
		byID[a.ID] = a
	}
	c.alerts.mu.Unlock()
	if len(byID) == 0 {
		return
	}
	due, err := c.store.ListDueReminders(ctx, now.Unix())
	if err != nil || len(due) == 0 {
		return
	}
	channels, err := c.store.ListAlertChannels(ctx)
	if err != nil {
		return
	}
	for _, d := range due {
		a, ok := byID[d.AlertID]
		if !ok {
			continue
		}
		for _, ch := range channels {
			if ch.ID != d.ChannelID {
				continue
			}
			if err := c.store.AddAlertNotice(ctx, store.AddAlertNoticeParams{AlertID: a.ID, ChannelID: ch.ID, NotifiedAt: now.Unix()}); err != nil {
				c.log.Error("alerts: record notice", "err", err)
				continue
			}
			c.deliver(ch, apitypes.AlertNotification{Event: "reminder", Alert: toAlert(a), SentAt: now.UTC()})
		}
	}
}

// outbox queues notifications per channel. Each channel with something
// queued has one goroutine sending its queue in order (an alert's
// resolution never overtakes its opening), which exits once the queue is
// empty.
type outbox struct {
	mu     sync.Mutex
	queues map[int64][]apitypes.AlertNotification // by channel ID
}

// deliver queues n for ch and starts the channel's sender if it isn't
// running. A channel with maxQueued notifications waiting (it has been
// failing for a while) drops new ones and records that.
func (c *Core) deliver(ch store.AlertChannel, n apitypes.AlertNotification) {
	o := &c.alerts.outbox
	o.mu.Lock()
	q, running := o.queues[ch.ID]
	if len(q) >= maxQueued {
		o.mu.Unlock()
		c.channelFailed(c.ctx, ch.ID, errors.New("too many notifications waiting to be sent; this one was dropped"))
		return
	}
	o.queues[ch.ID] = append(q, n)
	o.mu.Unlock()
	if running {
		return
	}
	c.wg.Add(1)
	go c.sendQueue(ch.ID)
}

// sendQueue sends a channel's queued notifications in order until the
// queue is empty.
func (c *Core) sendQueue(id int64) {
	defer c.wg.Done()
	o := &c.alerts.outbox
	for {
		o.mu.Lock()
		q := o.queues[id]
		if len(q) == 0 || c.ctx.Err() != nil {
			delete(o.queues, id)
			o.mu.Unlock()
			return
		}
		n := q[0]
		o.mu.Unlock()
		c.sendWithRetries(id, n)
		o.mu.Lock()
		o.queues[id] = o.queues[id][1:]
		o.mu.Unlock()
	}
}

// sendWithRetries makes up to len(deliveryBackoff) attempts at one
// notification and records the outcome on the channel. A channel removed
// meanwhile isn't sent to.
func (c *Core) sendWithRetries(id int64, n apitypes.AlertNotification) {
	var err error
	for _, wait := range deliveryBackoff {
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-c.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		ch, gerr := c.store.GetAlertChannelByID(c.ctx, id)
		if gerr != nil {
			return // removed, or the daemon is closing
		}
		if err = c.send(c.ctx, ch, n); err == nil {
			c.channelSent(c.ctx, id)
			return
		}
		if c.ctx.Err() != nil {
			return
		}
	}
	c.log.Warn("alerts: delivery failed", "channel", id, "alert", n.Alert.Title, "err", err)
	c.channelFailed(c.ctx, id, err)
}

func (c *Core) channelSent(ctx context.Context, id int64) {
	if err := c.store.SetAlertChannelSent(ctx, store.SetAlertChannelSentParams{LastSentAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("alerts: record delivery", "err", err)
	}
}

func (c *Core) channelFailed(ctx context.Context, id int64, cause error) {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := c.store.SetAlertChannelFailed(ctx, store.SetAlertChannelFailedParams{LastError: msg, LastErrorAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("alerts: record delivery", "err", err)
	}
}

// send makes one delivery attempt.
func (c *Core) send(ctx context.Context, ch store.AlertChannel, n apitypes.AlertNotification) error {
	cfg, err := c.openChannel(ch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	switch apitypes.AlertChannelKind(ch.Kind) {
	case apitypes.ChannelWebhook:
		body, err := json.Marshal(n)
		if err != nil {
			return err
		}
		// Signed at the attempt, not at sent_at: receivers reject old
		// timestamps, and a retry goes out up to 20 minutes later.
		return c.alerts.post(ctx, cfg.URL, body, map[string]string{
			SignatureHeader: signWebhook(cfg.Secret, time.Now(), body), "Dock-Event": n.Event,
		})
	case apitypes.ChannelSlack:
		body, err := json.Marshal(map[string]string{"text": slackText(n)})
		if err != nil {
			return err
		}
		return c.alerts.post(ctx, cfg.URL, body, nil)
	case apitypes.ChannelEmail:
		if cfg.SMTP == nil {
			return errors.New("the channel has no SMTP settings")
		}
		subject, text := emailText(n)
		return sendMail(ctx, *cfg.SMTP, subject, text)
	}
	return fmt.Errorf("unknown channel kind %q", ch.Kind)
}

// signWebhook signs a webhook body: receivers recompute the HMAC-SHA256
// of "<t>.<body>" with the channel's secret, compare it in constant time
// and reject old timestamps, which stops replays.
func signWebhook(secret string, at time.Time, body []byte) string {
	t := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// newChannelClient is the HTTP client webhook and Slack channels post
// with. It never follows redirects: a channel's URL is where alerts go,
// nowhere else. It never connects to the server itself or to link-local
// addresses either (e.addrErr, checked after DNS), and keeps no idle
// connections: alerts are rare.
func newChannelClient(e *alertEngine) *http.Client {
	dial := func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		return e.addrErr(ap.Addr())
	}
	return &http.Client{
		Timeout:       deliveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: deliveryTimeout, Control: dial}).DialContext,
			TLSHandshakeTimeout: deliveryTimeout,
			DisableKeepAlives:   true,
		},
	}
}

// AllowLoopbackAlertChannels lets alert channels deliver to this server's
// own addresses, which they never may in production (alertEngine.addrErr).
// For tests whose receivers listen on 127.0.0.1.
func (c *Core) AllowLoopbackAlertChannels(allow bool) { c.alerts.loopback.Store(allow) }

// Cloud metadata services outside 169.254.0.0/16: AWS over IPv6 and
// Alibaba Cloud.
var (
	awsMetadataV6   = netip.MustParseAddr("fd00:ec2::254")
	alibabaMetadata = netip.MustParseAddr("100.100.100.200")
)

// addrErr says why alerts can't be sent to ip, or nil. Loopback would
// reach the daemon's own loopback-only services (the internal listener,
// the registry) past the app firewall; link-local holds cloud metadata
// (169.254.169.254). Private, CGNAT and tailnet addresses are fine: a
// homelab's ntfy or chat bridge lives there (ADR 0020).
func (e *alertEngine) addrErr(ip netip.Addr) error {
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback() || ip.IsUnspecified():
		if e.loopback.Load() {
			return nil
		}
		return fmt.Errorf("%s is this server itself; alerts go to other hosts (use the receiver's LAN, tailnet or public address)", ip)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip == awsMetadataV6 || ip == alibabaMetadata:
		return fmt.Errorf("%s is a link-local, multicast or cloud metadata address; alerts can't go there", ip)
	}
	return nil
}

// post sends a JSON body; anything but a 2xx answer is an error.
func (e *alertEngine) post(ctx context.Context, target string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dock/"+version.Version)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a token; don't repeat it
		}
		return fmt.Errorf("POST to the channel's URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("the channel's URL answered %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// headline is a notification's one-line summary.
func headline(n apitypes.AlertNotification) string {
	a := n.Alert
	switch n.Event {
	case "resolved":
		return "[resolved] " + a.Title
	case "reminder":
		return "[" + string(a.Severity) + ", still open] " + a.Title
	case "test":
		return "[test] " + a.Title
	}
	return "[" + string(a.Severity) + "] " + a.Title
}

// slackText is a Slack message, escaped: alert messages quote release
// errors and build output, which must not turn into @channel mentions or
// disguised links (<https://...|text>).
func slackText(n apitypes.AlertNotification) string {
	text := headline(n)
	if n.Alert.Message != "" && n.Event != "resolved" {
		text += "\n" + n.Alert.Message
	}
	return slackEscaper.Replace(text)
}

// slackEscaper escapes Slack's control characters.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func emailText(n apitypes.AlertNotification) (subject, text string) {
	a := n.Alert
	var b strings.Builder
	b.WriteString(a.Title + "\n\n")
	if a.Message != "" {
		b.WriteString(a.Message + "\n\n")
	}
	for _, kv := range [][2]string{{"Project", a.Project}, {"Environment", a.Environment}, {"Service", a.Service}, {"Server", a.Server}} {
		if kv[1] != "" {
			b.WriteString(kv[0] + ": " + kv[1] + "\n")
		}
	}
	b.WriteString("Opened: " + a.OpenedAt.UTC().Format(time.RFC1123) + "\n")
	if a.ResolvedAt != nil {
		b.WriteString("Resolved: " + a.ResolvedAt.UTC().Format(time.RFC1123) + "\n")
	}
	b.WriteString("\nSee every open alert with `dock alerts`.\n")
	return "dock " + headline(n), b.String()
}

// sendMail sends one plain-text mail. Port 465 is TLS from the start;
// on other ports STARTTLS is used when offered, and required before a
// password is sent.
func sendMail(ctx context.Context, s apitypes.SMTPSettings, subject, text string) error {
	port := s.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP %s: %w", addr, err)
	}
	defer func() { _ = cl.Close() }()
	if port != 465 {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("SMTP STARTTLS: %w", err)
			}
		} else if s.Username != "" {
			return fmt.Errorf("%s doesn't offer STARTTLS; not sending the password unencrypted (use port 465 for TLS)", addr)
		}
	}
	if s.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("SMTP login: %w", err)
		}
	}
	// The envelope takes bare addresses; From and To may carry names
	// ("Dock <dock@example.com>").
	if err := cl.Mail(envelopeAddr(s.From)); err != nil {
		return fmt.Errorf("SMTP sender: %w", err)
	}
	for _, to := range s.To {
		if err := cl.Rcpt(envelopeAddr(to)); err != nil {
			return fmt.Errorf("SMTP recipient %s: %w", to, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	var msg strings.Builder
	for _, h := range [][2]string{
		{"From", s.From}, {"To", strings.Join(s.To, ", ")}, {"Subject", mime.QEncoding.Encode("utf-8", oneLine(subject))},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)}, {"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
	} {
		msg.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	msg.WriteString("\r\n" + strings.ReplaceAll(text, "\n", "\r\n"))
	if _, err := io.WriteString(w, msg.String()); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	return cl.Quit()
}

// envelopeAddr is the bare address of a checked email address
// (channelSettings parsed it when the channel was added).
func envelopeAddr(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		return a.Address
	}
	return addr
}

// oneLine keeps a header value on one line.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func (c *Core) openChannel(ch store.AlertChannel) (channelConfig, error) {
	var cfg channelConfig
	plain, err := c.box.Open(ch.Config, sealedAlertChannel.AAD(ch.Name))
	if err != nil {
		return cfg, fmt.Errorf("open channel settings: %w", err)
	}
	return cfg, json.Unmarshal(plain, &cfg)
}

func toChannel(ch store.AlertChannel) apitypes.AlertChannel {
	out := apitypes.AlertChannel{
		Name: ch.Name, Kind: apitypes.AlertChannelKind(ch.Kind), Target: ch.Target,
		MinSeverity: apitypes.AlertSeverity(ch.MinSeverity), RenotifyMinutes: int(ch.RenotifyMinutes),
		CreatedAt: ch.CreatedAt, LastSentAt: ch.LastSentAt, LastErrorAt: ch.LastErrorAt, Failures: int(ch.Failures),
	}
	if ch.LastErrorAt != nil {
		out.LastError = ch.LastError
	}
	return out
}

// ListAlertChannels lists the channels, without their secrets. Scope:
// operators.
func (c *Core) ListAlertChannels(ctx context.Context, a Actor) ([]apitypes.AlertChannel, error) {
	if err := a.requireOperator("reading alert channels"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListAlertChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.AlertChannel, len(rows))
	for i, r := range rows {
		out[i] = toChannel(r)
	}
	return out, nil
}

// CreateAlertChannel adds a channel. A webhook without a secret gets a
// generated one, returned only here. Operators only.
func (c *Core) CreateAlertChannel(ctx context.Context, a Actor, req apitypes.CreateAlertChannelRequest) (apitypes.CreatedAlertChannel, error) {
	if err := a.requireOperator("adding alert channels"); err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	if err := validName("channel", req.Name); err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	cfg, target, generated, err := c.alerts.channelSettings(req)
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	sev := req.MinSeverity
	if sev == "" {
		sev = apitypes.SeverityWarning
	}
	if sev != apitypes.SeverityWarning && sev != apitypes.SeverityCritical {
		return apitypes.CreatedAlertChannel{}, invalid("min severity must be warning or critical")
	}
	renotify := defaultRenotify
	if req.RenotifyMinutes != nil {
		renotify = *req.RenotifyMinutes
	}
	if renotify < 0 || renotify > 7*24*60 {
		return apitypes.CreatedAlertChannel{}, invalid("renotify minutes must be 0 (never) to 10080 (a week)")
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	sealed, err := c.box.Seal(plain, sealedAlertChannel.AAD(req.Name))
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	row, err := c.store.CreateAlertChannel(ctx, store.CreateAlertChannelParams{
		Name: req.Name, Kind: string(req.Kind), Target: target, Config: sealed, MinSeverity: string(sev),
		RenotifyMinutes: int64(renotify), CreatedAt: now(),
	})
	if store.IsUnique(err) {
		return apitypes.CreatedAlertChannel{}, conflict("alert channel %q exists; remove it first to change it", req.Name)
	}
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	c.audit(ctx, a.platform(), "alert.channel_add", nil, nil, "alert channel "+req.Name,
		fmt.Sprintf("added %s alert channel %s to %s (%s and up)", req.Kind, req.Name, target, sev))
	return apitypes.CreatedAlertChannel{Channel: toChannel(row), Secret: generated}, nil
}

// channelSettings validates a new channel's settings and returns what is
// sealed, what lists show, and a generated webhook secret.
func (e *alertEngine) channelSettings(req apitypes.CreateAlertChannelRequest) (cfg channelConfig, target, generated string, err error) {
	switch req.Kind {
	case apitypes.ChannelWebhook, apitypes.ChannelSlack:
		u, perr := url.Parse(req.URL)
		if req.URL == "" || perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return cfg, "", "", invalid("a %s channel needs --url, an http:// or https:// URL", req.Kind)
		}
		if req.SMTP != nil {
			return cfg, "", "", invalid("SMTP settings are for email channels")
		}
		// Named hosts are checked when delivering (newChannelClient), after DNS.
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			host = "127.0.0.1"
		}
		if ip, perr := netip.ParseAddr(host); perr == nil {
			if err := e.addrErr(ip); err != nil {
				return cfg, "", "", invalid("%v", err)
			}
		}
		cfg.URL, target = req.URL, u.Scheme+"://"+u.Host
		if req.Kind == apitypes.ChannelWebhook {
			cfg.Secret = req.Secret
			if cfg.Secret == "" {
				b := make([]byte, 32)
				_, _ = rand.Read(b)
				cfg.Secret = hex.EncodeToString(b)
				generated = cfg.Secret
			}
		} else if req.Secret != "" {
			return cfg, "", "", invalid("only webhook channels are signed; slack channels take no secret")
		}
	case apitypes.ChannelEmail:
		s := req.SMTP
		if s == nil || s.Host == "" || s.From == "" || len(s.To) == 0 {
			return cfg, "", "", invalid("an email channel needs an SMTP host, a from address and at least one recipient")
		}
		if req.URL != "" || req.Secret != "" {
			return cfg, "", "", invalid("email channels take SMTP settings, not a URL or secret")
		}
		for _, addr := range append([]string{s.From}, s.To...) {
			if _, perr := mail.ParseAddress(addr); perr != nil || strings.ContainsAny(addr, "\r\n") {
				return cfg, "", "", invalid("%q is not an email address", addr)
			}
		}
		smtpCopy := *s
		cfg.SMTP, target = &smtpCopy, strings.Join(s.To, ", ")
	default:
		return cfg, "", "", invalid("channel kind must be webhook, slack or email")
	}
	return cfg, target, generated, nil
}

// RemoveAlertChannel deletes a channel; notifications it is retrying stop
// with the next attempt. Operators only.
func (c *Core) RemoveAlertChannel(ctx context.Context, a Actor, name string) error {
	if err := a.requireOperator("removing alert channels"); err != nil {
		return err
	}
	ch, err := c.channel(ctx, name)
	if err != nil {
		return err
	}
	if err := c.store.DeleteAlertChannel(ctx, ch.ID); err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "alert.channel_remove", nil, nil, "alert channel "+name, "removed alert channel "+name)
	return nil
}

// TestAlertChannel sends a test notification once, without retries, and
// reports how it went; the outcome is recorded on the channel like any
// delivery. Operators only.
func (c *Core) TestAlertChannel(ctx context.Context, a Actor, name string) (apitypes.AlertChannelTest, error) {
	if err := a.requireOperator("testing alert channels"); err != nil {
		return apitypes.AlertChannelTest{}, err
	}
	ch, err := c.channel(ctx, name)
	if err != nil {
		return apitypes.AlertChannelTest{}, err
	}
	t := now()
	n := apitypes.AlertNotification{Event: "test", SentAt: t, Alert: apitypes.Alert{
		Kind: "test", Severity: apitypes.SeverityWarning, Title: "Test alert from dock",
		Message: "Channel " + name + " works. Alerts at " + ch.MinSeverity + " and up come here.", OpenedAt: t, UpdatedAt: t,
	}}
	out := apitypes.AlertChannelTest{OK: true}
	if err := c.send(ctx, ch, n); err != nil {
		out.OK, out.Error = false, err.Error()
		c.channelFailed(ctx, ch.ID, err)
	} else {
		c.channelSent(ctx, ch.ID)
	}
	if ch, err = c.store.GetAlertChannelByID(ctx, ch.ID); err != nil {
		return out, err
	}
	out.Channel = toChannel(ch)
	return out, nil
}

func (c *Core) channel(ctx context.Context, name string) (store.AlertChannel, error) {
	ch, err := c.store.GetAlertChannel(ctx, name)
	if store.IsNotFound(err) {
		return ch, notFound("alert channel %q not found; `dock alerts channel list` lists them", name)
	}
	return ch, err
}
