# Getting started

From a fresh server to a live app with a domain. Assumes the server is
[installed](install.md) and you have the admin password it printed.

Your app needs a `Dockerfile` at the repo root, or be something
[Railpack](https://railpack.com) can build (Node, Python, Go, ...). It should
listen on `$PORT` (default `8080`).

```sh
# 1. Log in: opens the browser, where you sign in and approve the code
#    (saved as a context in ~/.config/dock/config.json)
dock login http://100.64.0.10:7070

# 2. A project. It comes with a `production` environment
dock project create shop

# 3. A service in it. Creating doesn't deploy anything
dock service create web --project shop --environment production

# 4. Link this directory, so later commands need no flags
cd ~/code/shop
dock link --project shop --environment production --service web

# 5. Variables for the service (staged; the deploy takes them live)
dock env set NODE_ENV=production SESSION_SECRET=change-me

# 6. Upload, build, health-check, go live. Streams progress
dock deploy

# 7. A hostname under the server's base domain, no DNS work
dock domain generate

# 8. Your own domain. Prints the DNS record to create
dock domain add shop.example.com

# 9. Check it
dock status
dock logs --follow
```

On a terminal, `dock link` without flags offers pickers instead. Without a
link, `dock deploy -p shop -e production -s web` also creates a missing
service, so step 3 is optional there.

What happens on step 6:

```
queued → building → starting → health_checking → active
```

If the build or health check fails, `dock deploy` exits with code 6 and
nothing changed: the previous release (if any) is still serving. See
[deploying](deploying.md).

Step 7 needs `base_domain` in the server's config; without it, skip to step 8.
Step 8 is live once DNS points at the server; DNS can be set before or after.
See [domains](domains.md).

## Settings that live in the repo

Optional `dock.toml` at the repo root, read on every deploy:

```toml
[deploy]
port = 3000
health_check_path = "/healthz"
```

Full reference: [dock.toml](../dock-toml.md).

## Next

- A `staging` environment: `dock environment create staging`, then link a
  second directory or pass `--environment staging`.
- Let an agent deploy to staging: [agents](agents.md).
- Roll back: `dock rollback`.
