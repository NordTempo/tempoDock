# Tempo Dock's landing site. The build context is site/ (Dock deploys it with
# `dock git connect NordTempo/tempoDock --dir site`).
#
# The docs pages render docs/guide, which is outside that context, so the
# build takes the directory from the repository's main branch: there is no
# copy of the guide under site/. BuildKit fetches the archive again when
# main has moved.
# Bases are pinned by digest; the tag is for humans (Dependabot moves both).
FROM node:24-alpine@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5 AS build
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

FROM caddy:2-alpine@sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/dist /srv
EXPOSE 8080
