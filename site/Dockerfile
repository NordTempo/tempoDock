# Tempo Dock's landing site. Build context is the repo root (Dock builds with
# the Dockerfile path site/Dockerfile).
FROM node:24-alpine AS build
WORKDIR /site
RUN corepack enable
COPY site/package.json site/pnpm-lock.yaml site/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY site/astro.config.ts site/tsconfig.json ./
COPY site/public ./public
COPY site/src ./src
RUN pnpm build

FROM caddy:2-alpine
COPY site/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/dist /srv
EXPOSE 8080
