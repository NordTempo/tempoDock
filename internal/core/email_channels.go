package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Email alert channels send through Dock's email server (ADR 0079): a
// channel holds only its recipients, and each notification is a mail per
// recipient in the outbox, retried there. The channel's status is its
// mails' (emailDone).

// mailChannel queues notification n for an email channel's recipients.
// While email isn't set up, or the outbox refuses, the channel records
// the failure.
func (c *Core) mailChannel(ctx context.Context, ch store.AlertChannel, n apitypes.AlertNotification) {
	cfg, err := c.openChannel(ch)
	if err == nil && len(cfg.To) == 0 {
		err = fmt.Errorf("alert channel %s has no recipients; remove it and add it again", ch.Name)
	}
	if err != nil {
		c.channelFailed(ctx, ch.ID, err)
		return
	}
	subject, text := emailText(n)
	var org int64
	if ch.OrgID != nil {
		org = *ch.OrgID
	}
	for _, to := range cfg.To {
		m := outgoing{kind: apitypes.EmailAlert, orgID: org, ref: "channel:" + strconv.FormatInt(ch.ID, 10), to: to, content: mail.Content{Subject: subject, Text: text}}
		if _, err := c.queueNow(ctx, m); err != nil {
			c.channelFailed(ctx, ch.ID, err)
			return
		}
	}
}

// mailAddresses is a channel's recipients as mail addresses; they were
// checked when the channel was added.
func mailAddresses(to []string) []mail.Address {
	out := make([]mail.Address, len(to))
	for i, a := range to {
		out[i] = mail.Address{Address: a}
	}
	return out
}

// moveEmailChannels moves email channels that carry an SMTP server of
// their own (added before ADR 0079) onto Dock's email server, on start.
// A platform channel's server becomes the platform's when the platform
// has none yet: the operator set it up for this. Every channel keeps only
// its recipients. Each move is audited in its owner's log; a channel that
// doesn't open is logged and left as it is.
func (c *Core) moveEmailChannels(ctx context.Context) error {
	rows, err := c.store.ListEmailChannels(ctx)
	if err != nil {
		return err
	}
	for _, ch := range rows {
		cfg, err := c.openChannel(ch)
		if err != nil {
			c.log.Warn("email channels: open a channel", "channel", ch.Name, "err", err)
			continue
		}
		if cfg.SMTP == nil {
			continue
		}
		old := *cfg.SMTP
		adopted := false
		if ch.OrgID == nil {
			if _, err := c.store.GetEmailSettings(ctx); store.IsNotFound(err) {
				if err := c.adoptChannelServer(ctx, old); err != nil {
					c.log.Warn("email channels: use a channel's server for Dock's email", "channel", ch.Name, "err", err)
				} else {
					adopted = true
					c.audit(ctx, System, "platform.email_set", nil, nil, "email",
						fmt.Sprintf("set Dock's email to send through %s, from alert channel %s", old.Host, ch.Name))
				}
			}
		}
		plain, err := json.Marshal(channelConfig{To: old.To})
		if err != nil {
			return err
		}
		sealed, err := c.box.Seal(plain, channelAAD(ch.OrgID, ch.Name))
		if err != nil {
			return err
		}
		if err := c.store.SetAlertChannelConfig(ctx, store.SetAlertChannelConfigParams{Config: sealed, Target: strings.Join(old.To, ", "), ID: ch.ID}); err != nil {
			return err
		}
		a := System
		if ch.OrgID != nil {
			a = System.InOrg(*ch.OrgID)
		}
		summary := fmt.Sprintf("alert channel %s now sends through Dock's email server; its own SMTP server (%s) was removed", ch.Name, old.Host)
		if adopted {
			summary = fmt.Sprintf("alert channel %s's SMTP server (%s) is now Dock's email server, which the channel sends through", ch.Name, old.Host)
		}
		c.audit(ctx, a, "alert.channel_email", nil, nil, "alert channel "+ch.Name, summary)
	}
	return nil
}

// adoptChannelServer makes an old email channel's SMTP server the
// platform's: port 465 TLS, others STARTTLS, as the channel sent.
func (c *Core) adoptChannelServer(ctx context.Context, s legacySMTP) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return err
	}
	sec, port := apitypes.EmailStartTLS, cmp.Or(s.Port, 587)
	if port == 465 {
		sec = apitypes.EmailTLS
	}
	password := []byte{}
	if s.Username != "" && s.Password != "" {
		if password, err = c.box.Seal([]byte(s.Password), emailPasswordAAD); err != nil {
			return err
		}
	}
	return c.store.SetEmailSettings(ctx, store.SetEmailSettingsParams{
		Host: s.Host, Port: int64(port), Security: string(sec), Username: s.Username, Password: password,
		FromAddr: from.Address, FromName: from.Name, UpdatedBy: "system", UpdatedAt: now(),
	})
}
