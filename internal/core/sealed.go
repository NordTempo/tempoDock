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
	sealedVariableValue                = secrets.Column{Table: "variables", Name: "value", Key: []string{"environment_id", "coalesce(service_id, 0)", "key"}}
	sealedVariablePending              = secrets.Column{Table: "variables", Name: "pending_value", Slot: "variables.value", Key: sealedVariableValue.Key}
	sealedOrgVariable                  = secrets.Column{Table: "org_variables", Name: "value", Key: []string{"org_id", "key"}}
	sealedRegistrySecret               = secrets.Column{Table: "registry_credentials", Name: "secret", Key: []string{"org_id", "host"}}
	sealedReleaseConfig                = secrets.Column{Table: "releases", Name: "config", Key: []string{"service_id", "number"}}
	sealedAlertChannel                 = secrets.Column{Table: "alert_channels", Name: "config", Key: []string{"name"}}
	sealedLogDrain                     = secrets.Column{Table: "log_drains", Name: "config", Key: []string{"environment_id", "name"}}
	sealedGitSecret                    = secrets.Column{Table: "git_connections", Name: "secret", Key: []string{"service_id"}}
	sealedGitToken                     = secrets.Column{Table: "git_connections", Name: "token", Key: []string{"service_id"}}
	sealedAccessKey                    = secrets.Column{Table: "service_access", Name: "key", Key: []string{"service_id"}}
	sealedStorageKeys                  = secrets.Column{Table: "storage_connections", Name: "credentials", Key: []string{"name"}}
	sealedCloudflareToken              = secrets.Column{Table: "cloudflare_connections", Name: "token", Key: []string{"name"}}
	sealedTunnelToken                  = secrets.Column{Table: "cloudflare_tunnels", Name: "token", Key: []string{"tunnel_id"}} // the tunnel's, wherever its row points
	sealedAnalyticsSalt                = secrets.Column{Table: "analytics_salts", Name: "salt", Key: []string{"day"}}
	sealedBackupKeys                   = secrets.Column{Table: "backup_settings", Name: "credentials"}
	sealedBackupPassword               = secrets.Column{Table: "backup_settings", Name: "password"}
	sealedAnalyticsToken               = secrets.Column{Table: "analytics_settings", Name: "token"}
	sealedAppKey                       = secrets.Column{Table: "github_app", Name: "private_key"}
	sealedAppWebhookSecret             = secrets.Column{Table: "github_app", Name: "webhook_secret"}
	sealedDataReader, sealedDataWriter = datapanel.AccessColumns("service_id")
	// sealedUserFactor is a second factor's key (ADR 0057): one
	// authenticator app per user, passkeys by credential ID.
	sealedUserFactor = secrets.Column{Table: "user_factors", Name: "secret", Key: []string{"user_id", "kind", "coalesce(hex(credential_id), '')"}}

	sealedColumns = []secrets.Column{
		sealedVariableValue, sealedVariablePending, sealedOrgVariable, sealedReleaseConfig,
		sealedAlertChannel, sealedGitSecret, sealedGitToken, sealedAccessKey, sealedStorageKeys,
		sealedCloudflareToken, sealedTunnelToken, sealedAnalyticsSalt, sealedBackupKeys,
		sealedBackupPassword, sealedAnalyticsToken, sealedAppKey, sealedAppWebhookSecret,
		sealedDataReader, sealedDataWriter, sealedRequestArgs, sealedLogDrain, sealedRegistrySecret, sealedUserFactor,
	}
)

// The AADs of the one-row tables' values.
var (
	backupKeysAAD       = sealedBackupKeys.AAD()
	backupPasswordAAD   = sealedBackupPassword.AAD()
	analyticsTokenAAD   = sealedAnalyticsToken.AAD()
	appKeyAAD           = sealedAppKey.AAD()
	appWebhookSecretAAD = sealedAppWebhookSecret.AAD()
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

// secretsBound is install.secrets_format once every value is bound.
const secretsBound = 2

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
	c.box.RefuseUnbound()
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
