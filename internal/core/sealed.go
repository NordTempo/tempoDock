package core

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Sealed values are bound to where they are stored (ADR 0041): the AAD
// names the column and the row's key, so a value pasted into another row
// or column doesn't open. Keys are natural ones, known before a row is
// inserted and kept by the copies that move with it (a variable's staged
// value becoming its live one, a failed release restaging the values it
// carried); a value that moves to another row is opened and sealed again
// (an environment's clone, a renamed storage connection).
//
// sealedColumns is every sealed column of the store, for resealSecrets; a
// new sealed column goes here, and its AAD is made from its entry.
var (
	sealedVariableValue   = secrets.Column{Table: "variables", Name: "value", Key: []string{"environment_id", "coalesce(service_id, 0)", "key"}}
	sealedVariablePending = secrets.Column{Table: "variables", Name: "pending_value", Slot: "variables.value", Key: sealedVariableValue.Key}
	sealedOrgVariable     = secrets.Column{Table: "org_variables", Name: "value", Key: []string{"org_id", "key"}}
	sealedRegistrySecret  = secrets.Column{Table: "registry_credentials", Name: "secret", Key: []string{"org_id", "host"}}
	sealedReleaseConfig   = secrets.Column{Table: "releases", Name: "config", Key: []string{"service_id", "number"}}
	// A platform channel is named by its name, an org's by "<org>/<name>"
	// (channelAAD): platform channels kept the AAD they had before orgs.
	sealedAlertChannel    = secrets.Column{Table: "alert_channels", Name: "config", Key: []string{"ifnull(org_id || '/', '') || name"}}
	sealedLogDrain        = secrets.Column{Table: "log_drains", Name: "config", Key: []string{"environment_id", "name"}}
	sealedStatusHook      = secrets.Column{Table: "status_hooks", Name: "config", Key: []string{"page_id", "name"}}
	sealedGitSecret       = secrets.Column{Table: "git_connections", Name: "secret", Key: []string{"service_id"}}
	sealedGitToken        = secrets.Column{Table: "git_connections", Name: "token", Key: []string{"service_id"}}
	sealedAccessKey       = secrets.Column{Table: "service_access", Name: "key", Key: []string{"service_id"}}
	sealedStorageKeys     = secrets.Column{Table: "storage_connections", Name: "credentials", Key: []string{"name"}}
	sealedCloudflareToken = secrets.Column{Table: "cloudflare_connections", Name: "token", Key: []string{"ifnull(org_id || '/', '') || name"}} // as a channel's (cloudflareAAD)
	sealedTunnelToken     = secrets.Column{Table: "cloudflare_tunnels", Name: "token", Key: []string{"tunnel_id"}}                             // the tunnel's, wherever its row points
	sealedWildcardKey     = secrets.Column{Table: "wildcard_certificates", Name: "key", Key: []string{"base_domain"}}                          // ADR 0090
	sealedACMEKey         = secrets.Column{Table: "acme_accounts", Name: "key", Key: []string{"directory"}}
	sealedAnalyticsSalt   = secrets.Column{Table: "analytics_salts", Name: "salt", Key: []string{"day"}}
	sealedBackupKeys      = secrets.Column{Table: "backup_settings", Name: "credentials"}
	sealedBackupPassword  = secrets.Column{Table: "backup_settings", Name: "password"}
	sealedAnalyticsToken  = secrets.Column{Table: "analytics_settings", Name: "token"}
	sealedHeartbeatURL    = secrets.Column{Table: "heartbeat", Name: "url"} // ADR 0097
	// A GitHub App's values (ADR 0105), bound to its random hook ID. The
	// legacy* AADs are the one-row github_app table's, which the migrated
	// App's values carry until resealGitHubApps binds them.
	sealedAppKey                       = secrets.Column{Table: "github_apps", Name: "private_key", Key: []string{"hook_id"}}
	sealedAppWebhookSecret             = secrets.Column{Table: "github_apps", Name: "webhook_secret", Key: []string{"hook_id"}}
	sealedAppClientSecret              = secrets.Column{Table: "github_apps", Name: "client_secret", Key: []string{"hook_id"}} // its OAuth client's (ADR 0066)
	sealedDataReader, sealedDataWriter = datapanel.AccessColumns("service_id")
	// sealedUserFactor is a second factor's key (ADR 0057): one
	// authenticator app per user, passkeys by credential ID.
	sealedUserFactor = secrets.Column{Table: "user_factors", Name: "secret", Key: []string{"user_id", "kind", "coalesce(hex(credential_id), '')"}}
	// sealedSSOSecret is an identity provider's client secret (ADR 0065).
	sealedSSOSecret = secrets.Column{Table: "sso_providers", Name: "client_secret", Key: []string{"name"}}
	// sealedSessionIDToken is the ID token a single sign-on session signed
	// in with, for its logout's id_token_hint, bound to the session.
	sealedSessionIDToken = secrets.Column{Table: "sessions", Name: "sso_id_token", Key: []string{"hash"}}
	// sealedEmailPassword is the platform's SMTP password, and
	// sealedEmailBody a queued mail, bound to its row's nonce (ADR 0079).
	sealedEmailPassword = secrets.Column{Table: "email_settings", Name: "password"}
	sealedEmailBody     = secrets.Column{Table: "email_outbox", Name: "body", Key: []string{"nonce"}}

	sealedColumns = []secrets.Column{
		sealedVariableValue, sealedVariablePending, sealedOrgVariable, sealedReleaseConfig,
		sealedAlertChannel, sealedGitSecret, sealedGitToken, sealedAccessKey, sealedStorageKeys,
		sealedCloudflareToken, sealedTunnelToken, sealedAnalyticsSalt, sealedBackupKeys,
		sealedBackupPassword, sealedAnalyticsToken, sealedAppKey, sealedAppWebhookSecret, sealedAppClientSecret,
		sealedDataReader, sealedDataWriter, sealedRequestArgs, sealedLogDrain, sealedRegistrySecret, sealedUserFactor,
		sealedSSOSecret, sealedSessionIDToken, sealedStatusHook, sealedEmailPassword, sealedEmailBody, sealedWildcardKey, sealedACMEKey,
		sealedHeartbeatURL,
	}
)

// The AADs of the one-row tables' values.
var (
	backupKeysAAD             = sealedBackupKeys.AAD()
	backupPasswordAAD         = sealedBackupPassword.AAD()
	analyticsTokenAAD         = sealedAnalyticsToken.AAD()
	legacyAppKeyAAD           = secrets.Bind("github_app.private_key")
	legacyAppWebhookSecretAAD = secrets.Bind("github_app.webhook_secret")
	legacyAppClientSecretAAD  = secrets.Bind("github_app.client_secret")
	emailPasswordAAD          = sealedEmailPassword.AAD()
	heartbeatURLAAD           = sealedHeartbeatURL.AAD()
)

// variableAAD binds a variable's live and staged value (serviceID nil:
// shared). It is the AAD of the values a release carried, too.
func variableAAD(envID int64, serviceID *int64, key string) secrets.AAD {
	var svc int64
	if serviceID != nil {
		svc = *serviceID
	}
	return sealedVariableValue.AAD(strconv.FormatInt(envID, 10), strconv.FormatInt(svc, 10), key)
}

// factorAAD binds a user's second factor of kind; credentialID is a
// passkey's, nil for an authenticator app.
func factorAAD(userID int64, kind string, credentialID []byte) secrets.AAD {
	return sealedUserFactor.AAD(strconv.FormatInt(userID, 10), kind, strings.ToUpper(hex.EncodeToString(credentialID)))
}

// ssoSecretAAD binds the client secret of provider name.
func ssoSecretAAD(name string) secrets.AAD {
	return sealedSSOSecret.AAD(name)
}

// resealSSOSecrets seals the client secrets of the providers orgs had
// (ADR 0058), still bound to (org_id, name), again for their platform
// provider, once: migration 0078 lists them in sso_reseal. New runs it
// with resealSecrets. Each provider is resealed and unlisted in one
// transaction, so it is safe to interrupt or to run in two processes.
// A secret that doesn't open is left as it is, and logged.
func (c *Core) resealSSOSecrets(ctx context.Context) error {
	rows, err := c.store.ListSSOReseal(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.DeleteSSOReseal(ctx, r.SsoReseal.ProviderID); err != nil {
				return err
			}
			old := secrets.Column{Table: sealedSSOSecret.Table, Name: sealedSSOSecret.Name, Key: []string{"org_id", "name"}}
			secret, err := c.box.Open(r.SsoProvider.ClientSecret, old.AAD(strconv.FormatInt(r.SsoReseal.OrgID, 10), r.SsoReseal.Name))
			if err != nil {
				// Resealed by another process first, or unreadable: then
				// sign-ins through it fail until an operator sets it again.
				if _, again := c.box.Open(r.SsoProvider.ClientSecret, ssoSecretAAD(r.SsoProvider.Name)); again != nil {
					c.log.Warn("sso: can't open a client secret to reseal it; set it again with `dock sso set --client-secret`", "provider", r.SsoProvider.Name, "err", err)
				}
				return nil
			}
			sealed, err := c.box.Seal(secret, ssoSecretAAD(r.SsoProvider.Name))
			if err != nil {
				return err
			}
			return q.SetSSOProviderSecret(ctx, store.SetSSOProviderSecretParams{ClientSecret: sealed, ID: r.SsoProvider.ID})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// orgVariableAAD binds an org variable's value.
func orgVariableAAD(orgID int64, key string) secrets.AAD {
	return sealedOrgVariable.AAD(strconv.FormatInt(orgID, 10), key)
}

// registrySecretAAD binds an org's password or token for a registry host
// (ADR 0051).
func registrySecretAAD(orgID int64, host string) secrets.AAD {
	return sealedRegistrySecret.AAD(strconv.FormatInt(orgID, 10), host)
}

// releaseAAD binds a release's config.
func releaseAAD(serviceID, number int64) secrets.AAD {
	return sealedReleaseConfig.AAD(strconv.FormatInt(serviceID, 10), strconv.FormatInt(number, 10))
}

// gitSecretAAD and gitTokenAAD bind a service's git connection's webhook
// secret and GitHub token.
func gitSecretAAD(serviceID int64) secrets.AAD {
	return sealedGitSecret.AAD(strconv.FormatInt(serviceID, 10))
}

func gitTokenAAD(serviceID int64) secrets.AAD {
	return sealedGitToken.AAD(strconv.FormatInt(serviceID, 10))
}

// accessKeyAAD binds the key a service's login protection signs with.
func accessKeyAAD(serviceID int64) secrets.AAD {
	return sealedAccessKey.AAD(strconv.FormatInt(serviceID, 10))
}

// saltAAD binds an analytics day's salt.
func saltAAD(day int64) secrets.AAD {
	return sealedAnalyticsSalt.AAD(strconv.FormatInt(day, 10))
}

// appKeyAAD, appWebhookSecretAAD and appClientSecretAAD bind a GitHub
// App's private key, webhook secret and OAuth client secret to its row.
func appKeyAAD(hookID string) secrets.AAD           { return sealedAppKey.AAD(hookID) }
func appWebhookSecretAAD(hookID string) secrets.AAD { return sealedAppWebhookSecret.AAD(hookID) }
func appClientSecretAAD(hookID string) secrets.AAD  { return sealedAppClientSecret.AAD(hookID) }

// install.secrets_format: secretsBound once every value is bound,
// secretsAppsBound once the GitHub App migrated into github_apps is bound
// to its row too (resealGitHubApps).
const (
	secretsBound     = 2
	secretsAppsBound = 3
)

// resealSecrets binds the values sealed before ADR 0041 to where they are
// stored, once per database, then has the box refuse unbound values. New
// runs it before anything reads a value. It is idempotent and safe to
// interrupt or to run in two processes at once (an admin command next to
// the daemon; secrets.Box.ResealColumns).
func (c *Core) resealSecrets(ctx context.Context) error {
	format, err := c.store.GetSecretsFormat(ctx)
	if err != nil {
		return err
	}
	if format < secretsBound {
		if err := c.box.ResealColumns(ctx, c.store.DB, sealedColumns, c.log); err != nil {
			return err
		}
		if err := c.resealCarried(ctx); err != nil {
			return err
		}
		if err := c.store.SetSecretsFormat(ctx, secretsBound); err != nil {
			return err
		}
	}
	if format < secretsAppsBound {
		if err := c.resealGitHubApps(ctx); err != nil {
			return err
		}
		if err := c.store.SetSecretsFormat(ctx, secretsAppsBound); err != nil {
			return err
		}
	}
	c.box.RefuseUnbound()
	return nil
}

// resealGitHubApps binds the values of the GitHub App migration 0134
// moved into github_apps to its row: they were sealed for the one-row
// github_app table. A value that already opens bound to its row (resealed
// by an earlier run, or bound from unsealed by ResealColumns) is left as
// it is, so this is idempotent; one that opens neither way is left to
// fail when read, as ResealColumns does.
func (c *Core) resealGitHubApps(ctx context.Context) error {
	apps, err := c.store.ListGitHubApps(ctx)
	if err != nil {
		return err
	}
	for _, r := range apps {
		app := r.GithubApp
		changed := false
		for _, v := range []struct {
			b             *[]byte
			bound, legacy secrets.AAD
		}{
			{&app.PrivateKey, appKeyAAD(app.HookID), legacyAppKeyAAD},
			{&app.WebhookSecret, appWebhookSecretAAD(app.HookID), legacyAppWebhookSecretAAD},
			{&app.ClientSecret, appClientSecretAAD(app.HookID), legacyAppClientSecretAAD},
		} {
			if *v.b == nil {
				continue
			}
			if _, err := c.box.Open(*v.b, v.bound); err == nil {
				continue
			}
			plain, err := c.box.Open(*v.b, v.legacy)
			if err != nil {
				c.log.Warn("secrets: a GitHub App's value doesn't open", "app", app.Slug, "err", err)
				continue
			}
			if *v.b, err = c.box.Seal(plain, v.bound); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			continue
		}
		if err := c.store.SetGitHubAppSealed(ctx, store.SetGitHubAppSealedParams{
			PrivateKey: app.PrivateKey, WebhookSecret: app.WebhookSecret, ClientSecret: app.ClientSecret, ID: app.ID,
		}); err != nil {
			return err
		}
		c.log.Info("secrets: bound a GitHub App's values to its row", "app", app.Slug)
	}
	return nil
}

// resealCarried binds the variable values releases carried (carried.go)
// to their variables. Reseal gives a carried value the same bytes as the
// live variable it is a copy of, so restaging still finds it by bytes.
func (c *Core) resealCarried(ctx context.Context) error {
	rows, err := c.store.ListCarriedVariables(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		ch, err := decodeCarried(r.Carried)
		if err != nil {
			c.log.Warn("secrets: release's carried changes don't read", "release", r.ID, "err", err)
			continue
		}
		for i, v := range ch.Variables {
			aad := variableAAD(r.EnvironmentID, v.ServiceID, v.Key)
			for _, b := range []*[]byte{&ch.Variables[i].Old, &ch.Variables[i].New} {
				if *b == nil {
					continue
				}
				if *b, err = c.box.Reseal(*b, aad); err != nil {
					c.log.Warn("secrets: a release's carried value doesn't open; left unbound", "release", r.ID, "key", v.Key)
				}
			}
		}
		enc, err := ch.encode()
		if err != nil {
			return err
		}
		if enc == r.Carried {
			continue
		}
		if err := c.store.SetReleaseCarried(ctx, store.SetReleaseCarriedParams{Carried: enc, ID: r.ID, OldCarried: r.Carried}); err != nil {
			return fmt.Errorf("reseal release %d's carried changes: %w", r.ID, err)
		}
	}
	return nil
}
