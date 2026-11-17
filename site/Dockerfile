# Tempo Dock's landing site. The build context is site/ (Dock deploys it with
# `dock git connect NordTempo/tempoDock --dir site`).
#
# The docs pages render docs/guide, which is outside that context, so the
# build takes the directory from the repository's main branch: there is no
# copy of the guide under site/. BuildKit fetches the archive again when
# main has moved.
FROM node:24-alpine AS build
ADD https://github.com/NordTempo/tempoDock/archive/refs/heads/main.tar.gz /tmp/repo.tar.gz
RUN tar -xzf /tmp/repo.tar.gz -C / --strip-components=1 tempoDock-main/docs/guide && rm /tmp/repo.tar.gz
ENV GUIDE_DIR=/docs/guide
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
