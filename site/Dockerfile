# Tempo Dock's landing site. The build context is site/ (Dock deploys it with
# `dock git connect NordTempo/tempoDock --dir site`).
#
# The docs pages render docs/guide, which is outside that context, so the
# build takes the directory from the repository's main branch: there is no
# copy of the guide under site/. BuildKit fetches the archive again when
# main has moved.
# Bases are pinned by digest; the tag is for humans (Dependabot moves both).
FROM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS build
ADD https://github.com/NordTempo/tempoDock/archive/refs/heads/main.tar.gz /tmp/repo.tar.gz
RUN tar -xzf /tmp/repo.tar.gz -C / --strip-components=1 tempoDock-main/docs/guide && rm /tmp/repo.tar.gz
ENV GUIDE_DIR=/docs/guide
WORKDIR /site
# Node 25 and later don't bundle corepack; it comes from npm, and it
# installs the pnpm that package.json's packageManager names.
RUN npm install -g corepack@0.36.0 && corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY astro.config.ts tsconfig.json ./
COPY public ./public
COPY src ./src
RUN pnpm build

FROM caddy:2-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/dist /srv
EXPOSE 8080
