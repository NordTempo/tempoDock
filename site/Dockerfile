# Tempo Dock's landing site. The build context is site/ (Dock deploys it with
# `dock git connect NordTempo/tempoDock --dir site`).
FROM node:24-alpine AS build
WORKDIR /site
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY astro.config.ts tsconfig.json ./
COPY public ./public
COPY src ./src
RUN pnpm build

FROM caddy:2-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/dist /srv
EXPOSE 8080
