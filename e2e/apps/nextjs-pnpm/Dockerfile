# The Next.js example Dockerfile for pnpm (deps, builder, runner), without a
# cache mount for .next/cache: every build compiles from scratch.
FROM node:22-alpine AS base

FROM base AS deps
RUN apk add --no-cache libc6-compat
RUN npm install -g pnpm@10.20.0
WORKDIR /app
COPY package.json pnpm-lock.yaml ./
RUN pnpm i --frozen-lockfile

FROM base AS builder
RUN npm install -g pnpm@10.20.0
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN pnpm run build

FROM base AS runner
WORKDIR /app
ENV NODE_ENV=production
RUN addgroup --system --gid 1001 nodejs && adduser --system --uid 1001 nextjs
COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
RUN mkdir -p .next/cache && chown nextjs:nodejs .next/cache
RUN chmod -R a-w+x . && chmod -R a+x .next node_modules && chmod u+w .next/cache
USER nextjs
EXPOSE 3000
ENV PORT=3000 HOSTNAME=0.0.0.0
CMD ["node", "server.js"]
