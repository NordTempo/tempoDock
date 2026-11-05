# Seeds the freshly installed control plane with what a production install
# has: two projects with environments, services with plain, shared, org and
# referenced variables, generated and custom domains that route, a Postgres
# with a Dock volume and one without (data in the image's anonymous VOLUME),
# both holding rows, backups on a MinIO storage connection, an alert
# channel, a webhook git connection, analytics, API tokens and a browser
# session (the second half is seed2.sh). Uses only the CLI and API of the
# version installed (0.14+).
source /root/upg/lib.sh

PUBLIC_IP=$(awk -F'"' '/^public_ip/ {print $2}' /etc/dock/config.toml)
CUSTOM=(shop.example.com www.shop.example.com blog.example.org)
for h in "${CUSTOM[@]}" web-shop.upg.test; do
  grep -q " $h\$" /etc/hosts || echo "$PUBLIC_IP $h" >> /etc/hosts
done

say "org variables"
d org env set SHARED_TOKEN=org-secret-1234 SENTRY_DSN=https://k@sentry.example/1

say "project shop: production + staging"
d project create shop
d environment create staging -p shop --kind non-production
P=(-p shop -e production)

d service create db "${P[@]}" --template postgres
d service create rawpg "${P[@]}" --image postgres:17-alpine --port 5432
d env set "${P[@]}" -s rawpg POSTGRES_PASSWORD=rawpg-pass-5678
d service create web "${P[@]}" --image traefik/whoami:v1.10 --port 80 --health-check-path /health
d env set "${P[@]}" --shared LOG_LEVEL=info
d env set "${P[@]}" -s web SECRET_KEY=s3cr3t-web-key 'DATABASE_URL=${{db.DATABASE_URL}}' \
  'ORG_TOKEN=${{org.SHARED_TOKEN}}' 'RAW_PG=postgres://postgres:${{rawpg.POSTGRES_PASSWORD}}@rawpg:5432/postgres'
d apply "${P[@]}"
d deploy "${P[@]}" -s rawpg --image postgres:17-alpine
d deploy "${P[@]}" -s web --image traefik/whoami:v1.10
d domain generate "${P[@]}" -s web --detach
d domain add shop.example.com "${P[@]}" -s web --timeout 90s
d domain add www.shop.example.com "${P[@]}" -s web --redirect-to shop.example.com --timeout 90s

d service create web -p shop -e staging --image traefik/whoami:v1.10 --port 80
d env set -p shop -e staging -s web SECRET_KEY=staging-key
d apply -p shop -e staging
d deploy -p shop -e staging -s web --image traefik/whoami:v1.10

say "project blog"
d project create blog
d service create api -p blog -e production --image traefik/whoami:v1.10 --port 80
d env set -p blog -e production -s api API_KEY=blog-api-key-9 'ORG_DSN=${{org.SENTRY_DSN}}'
d apply -p blog -e production
d deploy -p blog -e production -s api --image traefik/whoami:v1.10
d domain add blog.example.org -p blog -e production -s api --timeout 90s
d git connect upg/fake-blog -p blog -e production -s api --mode webhook --branch main || true

bash /root/upg/seed2.sh
