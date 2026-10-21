// In-memory stand-in for the daemon API, used until the real endpoints land.
// Only ./queries.ts, ./mutations.ts and ./live.ts may import this file;
// components go through the hooks. Every read returns a copy so the query
// cache never shares objects with this "database".

import type {
  Actor,
  AuditEvent,
  AuditFilter,
  DeployStep,
  Domain,
  EditableSettings,
  Environment,
  LiveEvent,
  LogLevel,
  LogLine,
  NewChange,
  NewToken,
  Org,
  PendingChange,
  Problem,
  ProjectOverview,
  ProjectTree,
  Release,
  ReleaseChange,
  Server,
  Service,
  ServiceSource,
  Session,
  SettingChange,
  Token,
  User,
  Variable,
  VariableRow,
  VariableTarget,
  VariableValue,
  Volume,
} from "./types";

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const now = Date.now();
const ago = (ms: number) => new Date(now - ms).toISOString();
const stamp = () => new Date().toISOString();

export class NotFoundError extends Error {}

const saile: Actor = { kind: "user", name: "saile" };
const shopAgent: Actor = {
  kind: "token",
  name: "agent-shop-deploy",
  scope: "deploy",
  limit: "shop/*",
};
const ledgerCi: Actor = {
  kind: "token",
  name: "ci-ledger",
  scope: "deploy",
  limit: "ledger/staging",
};

// Servers

const servers: Server[] = [
  {
    id: "hetzner",
    name: "hetzner",
    role: "production",
    builder: false,
    address: "203.0.113.10",
    status: { state: "online" },
    cpuPercent: 18,
    memory: { usedMb: 5_900, totalMb: 16_384 },
    disk: { usedGb: 61, totalGb: 160, prunableGb: 4 },
    environmentCount: 3,
    version: "0.4.1",
  },
  {
    id: "homelab",
    name: "homelab",
    role: "non-production",
    builder: true,
    address: "198.51.100.24",
    status: { state: "online" },
    cpuPercent: 9,
    memory: { usedMb: 11_200, totalMb: 32_768 },
    disk: { usedGb: 412, totalGb: 490, prunableGb: 61 },
    environmentCount: 3,
    version: "0.4.1",
  },
];

const serverById = (id: string) => {
  const server = servers.find((s) => s.id === id);
  if (!server) throw new NotFoundError(`Server ${id} not found`);
  return server;
};

// Projects and environments

const projects = [
  { id: "shop", name: "shop" },
  { id: "ledger", name: "ledger" },
  { id: "site", name: "site" },
];

type EnvRow = Environment & { projectId: string };

function env(projectId: string, name: string): EnvRow {
  const server = serverById(name === "production" ? "hetzner" : "homelab");
  return {
    id: `${projectId}-${name}`,
    projectId,
    name,
    production: name === "production",
    server: { id: server.id, name: server.name, address: server.address },
  };
}

const environments: EnvRow[] = [
  env("shop", "production"),
  env("shop", "staging"),
  env("ledger", "production"),
  env("ledger", "staging"),
  env("site", "production"),
  env("site", "staging"),
];

const envById = (id: string) => {
  const found = environments.find((e) => e.id === id);
  if (!found) throw new NotFoundError(`Environment ${id} not found`);
  return found;
};

// Services

type ServiceSeed = {
  env: string;
  name: string;
  release: number;
  change: ReleaseChange;
  cpu: number;
  mem: [used: number, limit: number];
  updated: number;
  state?: Service["state"];
  source?: ServiceSource;
  volume?: Volume;
  port?: number;
};

const commit = (sha: string, message: string): ReleaseChange => ({
  kind: "commit",
  sha,
  message,
});

const pgImage: ReleaseChange = { kind: "image", ref: "postgres:16.4" };

function service(seed: ServiceSeed): Service {
  const environment = envById(seed.env);
  const isDb = seed.name === "postgres";
  const port = seed.port ?? (isDb ? 5432 : 8080);
  return {
    id: `${seed.env}-${seed.name}`,
    name: seed.name,
    projectId: environment.projectId,
    projectName: environment.projectId,
    environmentId: environment.id,
    environmentName: environment.name,
    server: { id: environment.server.id, name: environment.server.name },
    state: seed.state ?? { state: "active" },
    activeRelease: { number: seed.release, change: seed.change },
    imageDigest: `sha256:${hex(seed.env + seed.name, 4)}…${hex(seed.name, 4)}`,
    source:
      seed.source ??
      (isDb
        ? { kind: "image", ref: "postgres:16.4" }
        : {
            kind: "upload",
            dockerfile: `apps/${seed.name}/Dockerfile`,
            context: `apps/${seed.name}`,
          }),
    settings: {
      port,
      healthCheckPath: isDb ? "" : "/healthz",
      healthCheckIntervalS: 2,
      healthCheckTimeoutS: 30,
      cpuLimit: isDb ? 2 : 1,
      memoryLimitMb: seed.mem[1],
      strategy: seed.volume ? "recreate" : "rolling",
      restartPolicy: "unless-stopped",
    },
    volume: seed.volume ?? null,
    domains: { primary: null, count: 0, attention: 0 },
    privateAddress: `${seed.name}:${port}`,
    cpuPercent: seed.cpu,
    memory: { usedMb: seed.mem[0], peak24hMb: Math.round(seed.mem[0] * 2.6) },
    updatedAt: ago(seed.updated),
  };
}

// Deterministic fake hex so digests stay stable across reloads.
function hex(input: string, length: number) {
  let h = 2166136261;
  for (const ch of input) h = Math.imul(h ^ ch.charCodeAt(0), 16777619);
  return (h >>> 0).toString(16).padStart(8, "0").slice(0, length);
}

const pgdata = (sizeMb: number): Volume => ({
  name: "pgdata",
  mountPath: "/var/lib/postgresql/data",
  sizeMb,
});

const cart = commit("c90d1aa", "cart: saved carts");
const paginate = commit("3e81d0c", "orders: paginate list");

let services: Service[] = [
  service({
    env: "shop-production",
    name: "web",
    release: 57,
    change: commit("a41c9e2", "checkout: gift cards"),
    cpu: 4,
    mem: [168, 512],
    updated: 2 * HOUR,
    port: 3000,
  }),
  service({
    env: "shop-production",
    name: "api",
    release: 88,
    change: paginate,
    cpu: 11,
    mem: [231, 1024],
    updated: 2 * HOUR,
    state: { state: "deploying", release: 89 },
  }),
  service({
    env: "shop-production",
    name: "worker",
    release: 41,
    change: paginate,
    cpu: 2,
    mem: [96, 256],
    updated: 2 * HOUR,
  }),
  service({
    env: "shop-production",
    name: "postgres",
    release: 6,
    change: pgImage,
    cpu: 1,
    mem: [412, 2048],
    updated: 12 * DAY,
    volume: pgdata(2150),
  }),
  service({
    env: "shop-staging",
    name: "web",
    release: 58,
    change: cart,
    cpu: 1,
    mem: [120, 512],
    updated: 25 * MIN,
    port: 3000,
  }),
  service({
    env: "shop-staging",
    name: "api",
    release: 90,
    change: cart,
    cpu: 2,
    mem: [140, 1024],
    updated: 25 * MIN,
  }),
  service({
    env: "shop-staging",
    name: "worker",
    release: 43,
    change: cart,
    cpu: 0,
    mem: [0, 256],
    updated: 25 * MIN,
    state: {
      state: "crash_loop",
      restarts: 14,
      window: "10m",
      exit: "Exit 1: missing variable STRIPE_WEBHOOK_SECRET",
    },
  }),
  service({
    env: "shop-staging",
    name: "postgres",
    release: 3,
    change: pgImage,
    cpu: 1,
    mem: [188, 1024],
    updated: 12 * DAY,
    volume: pgdata(340),
  }),
  service({
    env: "ledger-production",
    name: "api",
    release: 34,
    change: commit("88f0c1b", "invoices: due date reminders"),
    cpu: 3,
    mem: [180, 512],
    updated: 3 * DAY,
  }),
  service({
    env: "ledger-production",
    name: "web",
    release: 21,
    change: commit("5d1e7a0", "reports: monthly export"),
    cpu: 1,
    mem: [90, 256],
    updated: 3 * DAY,
    port: 3000,
  }),
  service({
    env: "ledger-production",
    name: "postgres",
    release: 4,
    change: pgImage,
    cpu: 1,
    mem: [260, 1024],
    updated: 30 * DAY,
    volume: pgdata(820),
  }),
  service({
    env: "ledger-staging",
    name: "api",
    release: 36,
    change: commit("b7c2e19", "invoices: credit notes"),
    cpu: 1,
    mem: [150, 512],
    updated: 5 * HOUR,
  }),
  service({
    env: "ledger-staging",
    name: "web",
    release: 22,
    change: commit("b7c2e19", "invoices: credit notes"),
    cpu: 0,
    mem: [80, 256],
    updated: 5 * HOUR,
    port: 3000,
  }),
  service({
    env: "ledger-staging",
    name: "postgres",
    release: 2,
    change: pgImage,
    cpu: 0,
    mem: [120, 512],
    updated: 30 * DAY,
    volume: pgdata(96),
  }),
  service({
    env: "site-production",
    name: "web",
    release: 12,
    change: commit("41e0ad2", "blog: october post"),
    cpu: 0,
    mem: [40, 128],
    updated: 6 * DAY,
    port: 80,
    source: { kind: "git", repo: "nordtempo/site", branch: "main" },
  }),
  service({
    env: "site-staging",
    name: "web",
    release: 14,
    change: commit("9a03f5e", "nav: new footer"),
    cpu: 0,
    mem: [38, 128],
    updated: 1 * DAY,
    port: 80,
    source: { kind: "git", repo: "nordtempo/site", branch: "main" },
  }),
];

const serviceById = (id: string) => {
  const found = services.find((s) => s.id === id);
  if (!found) throw new NotFoundError(`Service ${id} not found`);
  return found;
};

// Releases

let releaseSeq = 0;

function release(
  serviceId: string,
  number: number,
  status: Release["status"],
  change: ReleaseChange,
  actor: Actor,
  age: number,
): Release {
  releaseSeq += 1;
  return {
    id: `rel_${releaseSeq}`,
    serviceId,
    number,
    status,
    change,
    actor,
    createdAt: ago(age),
    imageDigest: `sha256:${hex(`${serviceId}${number}`, 4)}…${hex(String(number), 4)}`,
  };
}

const apiId = "shop-production-api";

const releases: Release[] = [
  release(
    apiId,
    89,
    {
      state: "in_progress",
      steps: [
        {
          name: "build",
          state: "done",
          durationMs: 47_000,
          detail: "31 of 38 layers cached · sha256:4be1…c0e2",
        },
        {
          name: "start",
          state: "done",
          durationMs: 2_300,
          detail: "next to r88 on shop-production",
        },
        {
          name: "health_check",
          state: "running",
          durationMs: 14_000,
          detail: "GET :8080/healthz attempt 4/15 · 503 (timeout 30s)",
        },
        {
          name: "cutover",
          state: "pending",
          durationMs: null,
          detail: "route api.shop.nordtempo.no to r89, drain r88 10s",
        },
      ],
    },
    commit("7f3b012", "orders: idempotency keys"),
    shopAgent,
    MIN,
  ),
  release(apiId, 88, { state: "active" }, paginate, saile, 2 * HOUR),
  release(
    apiId,
    87,
    {
      state: "failed",
      reason:
        "Health check failed on /healthz after 30s (503). r86 kept serving.",
    },
    commit("9b2210f", "orders: bulk export"),
    shopAgent,
    3 * HOUR,
  ),
  release(
    apiId,
    86,
    { state: "superseded", imagePruned: false },
    { kind: "config", summary: "RATE_LIMIT_RPS 20 → 50" },
    saile,
    DAY,
  ),
  release(
    apiId,
    85,
    { state: "superseded", imagePruned: false },
    commit("51ac7e4", "auth: rotate session cookie"),
    shopAgent,
    2 * DAY,
  ),
  release(
    apiId,
    84,
    { state: "superseded", imagePruned: false },
    commit("e02f9a1", "deps: node 22.11"),
    saile,
    4 * DAY,
  ),
  release(
    apiId,
    83,
    { state: "superseded", imagePruned: true },
    commit("0c4d6b8", "orders: tax rounding"),
    saile,
    6 * DAY,
  ),
];

const olderCommits = [
  commit("d4a90b7", "deps: bump minor versions"),
  commit("16be0f3", "logging: structured request logs"),
  commit("f20c8d1", "ci: cache pnpm store"),
];

// Every other service gets its active release plus a few superseded ones.
for (const svc of services) {
  if (svc.id === apiId || !svc.activeRelease) continue;
  const { number, change } = svc.activeRelease;
  const actor = svc.projectId === "shop" ? shopAgent : ledgerCi;
  const age = now - Date.parse(svc.updatedAt);
  releases.push(
    release(svc.id, number, { state: "active" }, change, actor, age),
  );
  olderCommits.forEach((older, i) => {
    const n = number - i - 1;
    if (n < 1) return;
    releases.push(
      release(
        svc.id,
        n,
        { state: "superseded", imagePruned: i === 2 },
        svc.name === "postgres" ? pgImage : older,
        i % 2 ? saile : actor,
        age + (i + 1) * 2 * DAY,
      ),
    );
  });
}

// Domains

let domainSeq = 0;

function domain(
  serviceId: string,
  hostname: string,
  status: Domain["status"],
  generated = false,
): Domain {
  domainSeq += 1;
  const port = serviceById(serviceId).settings.port;
  return {
    id: `dom_${domainSeq}`,
    serviceId,
    hostname,
    generated,
    targetPort: port,
    status,
  };
}

const letsEncrypt = (renews: string): Domain["status"] => ({
  state: "active",
  certificate: `Let's Encrypt · renews ${renews}`,
});
const wildcard: Domain["status"] = {
  state: "active",
  certificate: "wildcard · generated",
};

const domains: Domain[] = [
  domain("shop-production-web", "shop.nordtempo.no", letsEncrypt("Dec 1")),
  domain("shop-production-web", "www.shop.nordtempo.no", letsEncrypt("Dec 1")),
  domain(apiId, "api.shop.nordtempo.no", letsEncrypt("Dec 1")),
  domain(apiId, "api-shop-production.nordtempo.app", wildcard, true),
  domain(apiId, "api.nordtempo.com", {
    state: "waiting_dns",
    record: { type: "A", name: "api.nordtempo.com", value: "203.0.113.10" },
    resolvesTo: null,
    checkedAt: ago(20_000),
    since: ago(18 * MIN),
  }),
  domain("shop-staging-web", "web-shop-staging.nordtempo.dev", wildcard, true),
  domain("shop-staging-api", "api-shop-staging.nordtempo.dev", wildcard, true),
  domain("ledger-production-web", "ledger.nordtempo.no", letsEncrypt("Nov 18")),
  domain(
    "ledger-production-api",
    "api.ledger.nordtempo.no",
    letsEncrypt("Nov 18"),
  ),
  domain(
    "ledger-staging-web",
    "web-ledger-staging.nordtempo.dev",
    wildcard,
    true,
  ),
  domain("site-production-web", "nordtempo.no", letsEncrypt("Jan 4")),
  domain("site-staging-web", "web-site-staging.nordtempo.dev", wildcard, true),
];

const needsAttention = (d: Domain) => d.status.state !== "active";

// Variables

type StoredVariable = Variable & {
  environmentId: string;
  serviceId: string | null;
};

const secretValues = new Map<string, string>();

function variable(
  environmentId: string,
  serviceId: string | null,
  name: string,
  value: VariableValue,
  secret?: string,
): StoredVariable {
  const id = `${serviceId ?? environmentId}:${name}`;
  if (secret) secretValues.set(id, secret);
  return {
    id,
    name,
    environmentId,
    serviceId,
    scope: serviceId ? "service" : "environment",
    value,
  };
}

const plain = (value: string): VariableValue => ({ kind: "plain", value });
const ref = (template: string, resolved: string): VariableValue => ({
  kind: "reference",
  template,
  resolved,
});
const secret: VariableValue = { kind: "secret" };
/** Reference syntax: use("postgres.DATABASE_URL") is ${{postgres.DATABASE_URL}}. */
const use = (path: string) => `\${{${path}}}`;

let variables: StoredVariable[] = [];

for (const e of environments) {
  variables.push(
    variable(
      e.id,
      null,
      "NODE_ENV",
      plain(e.production ? "production" : "staging"),
    ),
    variable(e.id, null, "LOG_LEVEL", plain("info")),
    variable(
      e.id,
      null,
      "SENTRY_DSN",
      secret,
      `https://${hex(e.id, 8)}@o1.ingest.sentry.io/42`,
    ),
  );
}

for (const svc of services) {
  const e = svc.environmentId;
  const db = `postgres://${svc.projectId}:•••@postgres:5432/${svc.projectId}`;
  const hasDb = services.some(
    (s) => s.environmentId === e && s.name === "postgres",
  );
  if (svc.name === "postgres") {
    variables.push(
      variable(e, svc.id, "POSTGRES_DB", plain(svc.projectId)),
      variable(e, svc.id, "POSTGRES_USER", plain(svc.projectId)),
      variable(
        e,
        svc.id,
        "POSTGRES_PASSWORD",
        secret,
        hex(`${svc.id}pw`, 8).repeat(3),
      ),
      variable(
        e,
        svc.id,
        "DATABASE_URL",
        ref(
          `postgres://${use("POSTGRES_USER")}:${use("POSTGRES_PASSWORD")}@postgres:5432/${use("POSTGRES_DB")}`,
          db,
        ),
      ),
    );
    continue;
  }
  variables.push(variable(e, svc.id, "PORT", plain(String(svc.settings.port))));
  if (hasDb && svc.name !== "web") {
    variables.push(
      variable(
        e,
        svc.id,
        "DATABASE_URL",
        ref(use("postgres.DATABASE_URL"), db),
      ),
    );
  }
}

const apiVars = (name: string, value: VariableValue, secretValue?: string) =>
  variable("shop-production", apiId, name, value, secretValue);

variables.push(
  apiVars(
    "WEB_ORIGIN",
    ref(`https://${use("web.DOMAIN")}`, "https://shop.nordtempo.no"),
  ),
  apiVars("STRIPE_SECRET_KEY", secret, "sk_live_51Nf0dE2xQ9rT"),
  apiVars("JWT_SECRET", secret, "f3b0c44298fc1c149afbf4c8996fb924"),
  apiVars("LOG_LEVEL", plain("debug")),
  apiVars("RATE_LIMIT_RPS", plain("50")),
  variable(
    "shop-staging",
    "shop-staging-worker",
    "STRIPE_SECRET_KEY",
    secret,
    "sk_test_51Nf0dE",
  ),
);

// Staged changes, keyed by environment.

let changeSeq = 0;
const nextChangeId = () => {
  changeSeq += 1;
  return `chg_${changeSeq}`;
};

const pending = new Map<string, PendingChange[]>([
  [
    "shop-production",
    [
      {
        id: nextChangeId(),
        kind: "variable.set",
        serviceId: apiId,
        name: "RATE_LIMIT_RPS",
        scope: "service",
        before: plain("50"),
        value: "80",
        secret: false,
      },
      {
        id: nextChangeId(),
        kind: "setting",
        serviceId: apiId,
        key: "memoryLimitMb",
        before: 1024,
        after: 768,
      },
    ],
  ],
]);

// Logs, newest first.

const logs = new Map<string, LogLine[]>();
let logSeq = 0;

function logLine(
  secondsAgo: number,
  release: number,
  level: LogLevel,
  message: string,
  stream: LogLine["stream"] = "app",
): LogLine {
  logSeq += 1;
  return {
    id: `log_${logSeq}`,
    at: new Date(now - secondsAgo * 1000).toISOString(),
    release,
    stream,
    level,
    message,
  };
}

logs.set(
  apiId,
  (
    [
      [0, 89, "warn", "healthz: migrations in progress, 503"],
      [0.2, 88, "info", "POST /orders 201 38ms"],
      [0.6, 88, "info", "GET /orders?page=2 200 12ms"],
      [2, 89, "warn", "healthz: migrations in progress, 503"],
      [2.5, 89, "info", "migrate 0042_idempotency_keys: 61%"],
      [3.8, 88, "info", "GET /products/918 200 4ms"],
      [4, 89, "warn", "healthz: migrations in progress, 503"],
      [5.1, 88, "info", "POST /cart/checkout 200 112ms"],
      [6, 89, "warn", "healthz: migrations in progress, 503"],
      [6.1, 89, "info", "migrate 0042_idempotency_keys: start"],
      [6.3, 89, "info", "connected postgres:5432/shop"],
      [6.4, 89, "info", "listening on :8080"],
      [6.6, 89, "info", "api 7f3b012 node 22.11.0"],
      [7.2, 88, "info", "GET /orders?page=1 200 15ms"],
      [7.7, 88, "error", "POST /webhooks/stripe 400 signature mismatch"],
      [10.1, 88, "info", "GET /healthz 200 1ms"],
      [11.3, 88, "info", "GET /products?tag=sale 200 22ms"],
      [12.8, 88, "info", "POST /orders 201 41ms"],
      [15, 88, "info", "GET /orders/88213 200 6ms"],
      [17.1, 88, "info", "GET /healthz 200 1ms"],
    ] as const
  ).map(([s, r, level, message]) => logLine(s, r, level, message)),
);

// Build output for r89, kept as its own stream.
logs.get(apiId)?.push(
  ...(
    [
      [14, "#1 [internal] load build definition from Dockerfile"],
      [20, "#6 [deps 2/3] RUN pnpm install --frozen-lockfile CACHED"],
      [40, "#11 [build 3/3] RUN pnpm build"],
      [58, "#14 exporting to image sha256:4be1…c0e2"],
      [61, "#15 pushing registry.local/shop/api:r89 done"],
    ] as const
  ).map(([s, message]) => logLine(s + 20, 89, "info", message, "build")),
);

logs.set(
  "shop-staging-worker",
  [0, 40, 80, 120].flatMap((s) => [
    logLine(s, 43, "error", "Error: missing variable STRIPE_WEBHOOK_SECRET"),
    logLine(s + 0.2, 43, "info", "worker c90d1aa starting, 4 queues"),
  ]),
);

function seedLogs(svc: Service) {
  const r = svc.activeRelease?.number ?? 1;
  const lines =
    svc.name === "postgres"
      ? ["checkpoint complete: wrote 312 buffers", "autovacuum: orders"]
      : ["GET /healthz 200 1ms", "GET / 200 8ms", "GET /assets/app.js 304 1ms"];
  return Array.from({ length: 12 }, (_, i) =>
    logLine(i * 9, r, "info", lines[i % lines.length] ?? ""),
  );
}

// Audit

let auditSeq = 0;

function audit(
  age: number,
  actor: Actor,
  summary: string,
  subject: string | null,
  serviceId: string | null,
  projectId: string | null = serviceId
    ? serviceById(serviceId).projectId
    : null,
): AuditEvent {
  auditSeq += 1;
  return {
    id: `aud_${auditSeq}`,
    at: ago(age),
    actor,
    summary,
    subject,
    projectId,
    serviceId,
  };
}

const auditLog: AuditEvent[] = [
  audit(MIN, shopAgent, "deploy api production r89", "api", apiId),
  audit(
    25 * MIN,
    saile,
    "add domain api.nordtempo.com to api production",
    "api",
    apiId,
  ),
  audit(
    26 * MIN,
    shopAgent,
    "deploy worker staging r43",
    "worker",
    "shop-staging-worker",
  ),
  audit(
    2 * HOUR,
    saile,
    "promote web staging r56 to production r57",
    "web",
    "shop-production-web",
  ),
  audit(2 * HOUR, saile, "deploy api production r88", "api", apiId),
  audit(
    3 * HOUR,
    shopAgent,
    "deploy api production r87 (failed)",
    "api",
    apiId,
  ),
  audit(
    5 * HOUR,
    ledgerCi,
    "deploy api staging r36",
    "api",
    "ledger-staging-api",
  ),
  audit(DAY, saile, "set RATE_LIMIT_RPS on api production", "api", apiId),
  audit(2 * DAY, saile, "create token agent-shop-deploy", null, null),
  audit(9 * DAY, saile, "add user kari", null, null),
];

// Org

const users: User[] = [
  {
    id: "usr_1",
    username: "saile",
    role: "admin",
    createdAt: ago(60 * DAY),
    lastSeenAt: ago(0),
  },
  {
    id: "usr_2",
    username: "kari",
    role: "member",
    createdAt: ago(9 * DAY),
    lastSeenAt: ago(2 * DAY),
  },
];

let tokens: Token[] = [
  {
    id: "tok_1",
    name: "agent-shop-deploy",
    scope: "deploy",
    limits: ["shop/*"],
    createdBy: "saile",
    createdAt: ago(2 * DAY),
    lastUsedAt: ago(MIN),
    expiresAt: ago(-28 * DAY),
  },
  {
    id: "tok_2",
    name: "ci-ledger",
    scope: "deploy",
    limits: ["ledger/staging"],
    createdBy: "saile",
    createdAt: ago(20 * DAY),
    lastUsedAt: ago(5 * HOUR),
    expiresAt: null,
  },
  {
    id: "tok_3",
    name: "grafana",
    scope: "read",
    limits: [],
    createdBy: "saile",
    createdAt: ago(40 * DAY),
    lastUsedAt: ago(30_000),
    expiresAt: null,
  },
  {
    id: "tok_4",
    name: "laptop",
    scope: "admin",
    limits: [],
    createdBy: "saile",
    createdAt: ago(50 * DAY),
    lastUsedAt: ago(3 * DAY),
    expiresAt: ago(-40 * DAY),
  },
];

// Session. Persisted in sessionStorage so a reload keeps the fake cookie.

const LOGGED_OUT = "dock.fixture.loggedOut";

// Live events

type Listener = (event: LiveEvent) => void;
const listeners = new Set<Listener>();
let ticker: ReturnType<typeof setInterval> | undefined;

function emit(event: LiveEvent) {
  for (const listener of listeners) listener(event);
}

function pushLog(serviceId: string, line: LogLine) {
  const lines = logs.get(serviceId);
  lines?.unshift(line);
  if (lines && lines.length > 2000) lines.length = 2000;
  emit({ type: "log.line", serviceId, line });
}

const trafficLines = [
  "GET /orders?page=1 200 14ms",
  "POST /orders 201 36ms",
  "GET /products/204 200 3ms",
  "GET /healthz 200 1ms",
];

/** Fake event stream: a request log line every couple of seconds. */
export function subscribe(listener: Listener) {
  listeners.add(listener);
  ticker ??= setInterval(() => {
    const line = trafficLines[logSeq % trafficLines.length] ?? "";
    pushLog(apiId, logLine(0, 88, "info", line));
  }, 2500);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      clearInterval(ticker);
      ticker = undefined;
    }
  };
}

// Reads

function withDomainSummary(svc: Service): Service {
  const own = domains.filter((d) => d.serviceId === svc.id);
  return {
    ...svc,
    domains: {
      primary:
        own.find((d) => !d.generated)?.hostname ?? own[0]?.hostname ?? null,
      count: own.length,
      attention: own.filter(needsAttention).length,
    },
  };
}

const deployingNumber = (svc: Service) =>
  svc.state.state === "deploying" ? svc.state.release : null;

function problemsFor(projectId: string): Problem[] {
  const own = services
    .filter((s) => s.projectId === projectId)
    .map(withDomainSummary);
  const bad: Problem[] = [];
  const warn: Problem[] = [];
  const run: Problem[] = [];
  for (const svc of own) {
    if (svc.state.state === "crash_loop") {
      bad.push({ kind: "crash_loop", service: svc, ...svc.state });
    }
    const latest = releases.find((r) => r.serviceId === svc.id);
    if (latest?.status.state === "failed") {
      bad.push({
        kind: "deploy_failed",
        service: svc,
        release: latest.number,
        reason: latest.status.reason,
      });
    }
    for (const d of domains) {
      if (d.serviceId === svc.id && d.status.state === "waiting_dns") {
        warn.push({
          kind: "dns_waiting",
          service: svc,
          domain: structuredClone(d),
          record: d.status.record,
        });
      }
    }
    const current =
      svc.state.state === "deploying"
        ? releases.find(
            (r) => r.serviceId === svc.id && r.number === deployingNumber(svc),
          )
        : undefined;
    if (current?.status.state === "in_progress") {
      const step = current.status.steps.find((s) => s.state === "running");
      run.push({
        kind: "deploying",
        service: svc,
        release: current.number,
        step: step ? stepDescription(step) : "queued",
      });
    }
  }
  const serverIds = new Set(own.map((s) => s.server.id));
  for (const server of servers) {
    if (
      serverIds.has(server.id) &&
      server.disk.usedGb / server.disk.totalGb >= 0.8
    ) {
      warn.push({ kind: "disk", server: structuredClone(server) });
    }
  }
  return [...bad, ...warn, ...run];
}

function stepDescription(step: DeployStep) {
  const attempt = /attempt (\d+)\/(\d+)/.exec(step.detail);
  const name = step.name.replace("_", " ");
  return attempt ? `${name}, attempt ${attempt[1]} of ${attempt[2]}` : name;
}

export const api = {
  async session(): Promise<Session | null> {
    return sessionStorage.getItem(LOGGED_OUT)
      ? null
      : { username: "saile", role: "admin" };
  },

  async login(username: string, password: string): Promise<Session> {
    const user = users.find((u) => u.username === username);
    // Fixture credentials: any listed user with password "dock".
    if (!user || password !== "dock") {
      throw new Error("Wrong username or password");
    }
    sessionStorage.removeItem(LOGGED_OUT);
    return { username: user.username, role: user.role };
  },

  async logout() {
    sessionStorage.setItem(LOGGED_OUT, "1");
  },

  async org(): Promise<Org> {
    return {
      name: "nordtempo",
      tokenCount: tokens.length,
      userCount: users.length,
    };
  },

  async projects(): Promise<ProjectTree[]> {
    return projects.map((p) => {
      const problems = problemsFor(p.id);
      const envs = environments.filter((e) => e.projectId === p.id);
      return {
        id: p.id,
        name: p.name,
        failing: problems.filter(
          (x) => x.kind === "crash_loop" || x.kind === "deploy_failed",
        ).length,
        // Server disk is shared by many projects; it shows under Servers.
        warning: problems.filter((x) => x.kind === "dns_waiting").length,
        serviceCount: services.filter((s) => s.projectId === p.id).length,
        environments: envs.map((e) => ({
          id: e.id,
          name: e.name,
          serverName: e.server.name,
          services: services
            .filter((s) => s.environmentId === e.id)
            .map((s) => ({
              id: s.id,
              name: s.name,
              state: structuredClone(s.state),
              release:
                s.state.state === "deploying"
                  ? s.state.release
                  : (s.activeRelease?.number ?? null),
            })),
        })),
      };
    });
  },

  async projectOverview(projectId: string): Promise<ProjectOverview> {
    const project = projects.find((p) => p.id === projectId);
    if (!project) throw new NotFoundError(`Project ${projectId} not found`);
    return structuredClone({
      project,
      environments: environments
        .filter((e) => e.projectId === projectId)
        .map(({ projectId: _, ...e }) => ({
          ...e,
          services: services
            .filter((s) => s.environmentId === e.id)
            .map(withDomainSummary),
        })),
      problems: problemsFor(projectId),
      recent: auditLog.filter((a) => a.projectId === projectId).slice(0, 5),
    });
  },

  async service(serviceId: string): Promise<Service> {
    return structuredClone(withDomainSummary(serviceById(serviceId)));
  },

  async releases(serviceId: string): Promise<Release[]> {
    serviceById(serviceId);
    return structuredClone(releases.filter((r) => r.serviceId === serviceId));
  },

  async logs(serviceId: string): Promise<LogLine[]> {
    const svc = serviceById(serviceId);
    if (!logs.has(serviceId)) logs.set(serviceId, seedLogs(svc));
    return structuredClone(logs.get(serviceId) ?? []);
  },

  async domains(serviceId: string): Promise<Domain[]> {
    return structuredClone(domains.filter((d) => d.serviceId === serviceId));
  },

  async variables(target: VariableTarget): Promise<VariableRow[]> {
    const envVars = variables.filter(
      (v) => v.environmentId === target.environmentId && v.serviceId === null,
    );
    const own = target.serviceId
      ? variables.filter((v) => v.serviceId === target.serviceId)
      : [];
    const ownNames = new Set(own.map((v) => v.name));
    const rows: VariableRow[] = [
      ...own.map((v) => ({
        ...toVariable(v),
        overrides: envVars.some((e) => e.name === v.name),
        overridden: false,
      })),
      ...envVars.map((v) => ({
        ...toVariable(v),
        overrides: false,
        overridden: ownNames.has(v.name),
      })),
    ];
    return structuredClone(rows);
  },

  async secretValues(target: VariableTarget): Promise<Record<string, string>> {
    const ids = variables
      .filter(
        (v) =>
          v.environmentId === target.environmentId &&
          (v.serviceId === null || v.serviceId === target.serviceId),
      )
      .map((v) => v.id);
    return Object.fromEntries(
      ids.flatMap((id) => {
        const value = secretValues.get(id);
        return value === undefined ? [] : [[id, value]];
      }),
    );
  },

  async pendingChanges(environmentId: string): Promise<PendingChange[]> {
    return structuredClone(pending.get(environmentId) ?? []);
  },

  async servers(): Promise<Server[]> {
    return structuredClone(servers);
  },

  async tokens(): Promise<Token[]> {
    return structuredClone(tokens);
  },

  async users(): Promise<User[]> {
    return structuredClone(users);
  },

  async audit(filter: AuditFilter): Promise<AuditEvent[]> {
    return structuredClone(
      auditLog.filter(
        (a) =>
          (!filter.projectId || a.projectId === filter.projectId) &&
          (!filter.serviceId || a.serviceId === filter.serviceId),
      ),
    );
  },

  // Writes

  async deploy(serviceId: string): Promise<Release> {
    const svc = serviceById(serviceId);
    assertIdle(svc);
    const of = svc.activeRelease?.number ?? 0;
    return startRelease(svc, { kind: "redeploy", of }, true);
  },

  async rollback(serviceId: string, to: number): Promise<Release> {
    const svc = serviceById(serviceId);
    assertIdle(svc);
    const target = releases.find(
      (r) => r.serviceId === serviceId && r.number === to,
    );
    if (target?.status.state === "superseded" && target.status.imagePruned) {
      throw new Error(`r${to} image was pruned, rollback is not possible`);
    }
    return startRelease(svc, { kind: "rollback", to }, false);
  },

  async restart(serviceId: string) {
    const svc = serviceById(serviceId);
    const r = svc.activeRelease?.number ?? 0;
    pushLog(serviceId, logLine(0, r, "info", `restart requested by saile`));
    record(`restart ${svc.name} ${svc.environmentName}`, svc);
  },

  async promote(projectId: string, from: string, to: string) {
    const target = services.filter(
      (s) => s.projectId === projectId && s.environmentName === to,
    );
    for (const source of services) {
      if (source.projectId !== projectId || source.environmentName !== from)
        continue;
      const dest = target.find((t) => t.name === source.name);
      if (!dest || dest.state.state === "deploying" || !source.activeRelease)
        continue;
      startRelease(
        dest,
        { kind: "promote", from, release: source.activeRelease.number },
        false,
      );
    }
  },

  async stageChange(environmentId: string, change: NewChange) {
    envById(environmentId);
    if (
      change.kind !== "setting" &&
      !/^[A-Za-z_][A-Za-z0-9_]*$/.test(change.name)
    ) {
      throw new Error(
        `${change.name || "Empty name"} is not a valid variable name`,
      );
    }
    const list = pending.get(environmentId) ?? [];
    // A new edit to the same variable or setting replaces the staged one.
    const same = (c: PendingChange) =>
      c.serviceId === change.serviceId &&
      (c.kind === "setting"
        ? change.kind === "setting" && c.key === change.key
        : change.kind !== "setting" &&
          c.name === change.name &&
          c.scope === change.scope);
    pending.set(environmentId, [
      ...list.filter((c) => !same(c)),
      { ...change, id: nextChangeId() },
    ]);
  },

  async discardChanges(environmentId: string, changeIds: string[]) {
    pending.set(
      environmentId,
      (pending.get(environmentId) ?? []).filter(
        (c) => !changeIds.includes(c.id),
      ),
    );
  },

  async applyChanges(environmentId: string, changeIds: string[]) {
    const all = pending.get(environmentId) ?? [];
    const applying = all.filter((c) => changeIds.includes(c.id));
    pending.set(
      environmentId,
      all.filter((c) => !changeIds.includes(c.id)),
    );
    for (const change of applying) commitChange(environmentId, change);

    const envWide = applying.some((c) => c.serviceId === null);
    const affected = services.filter(
      (s) =>
        s.environmentId === environmentId &&
        (envWide || applying.some((c) => c.serviceId === s.id)),
    );
    for (const svc of affected) {
      const summary = applying
        .filter((c) => c.serviceId === null || c.serviceId === svc.id)
        .map(changeSummary)
        .join(", ");
      startRelease(svc, { kind: "config", summary }, false);
    }
  },

  async addDomain(serviceId: string, hostname: string) {
    const svc = serviceById(serviceId);
    const name = hostname.trim().toLowerCase();
    if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(name)) {
      throw new Error(`${hostname} is not a valid hostname`);
    }
    if (domains.some((d) => d.hostname === name)) {
      throw new Error(`${name} already belongs to a service`);
    }
    const server = serverById(svc.server.id);
    domains.push(
      domain(serviceId, name, {
        state: "waiting_dns",
        record: { type: "A", name, value: server.address },
        resolvesTo: null,
        checkedAt: stamp(),
        since: stamp(),
      }),
    );
    record(`add domain ${name} to ${svc.name} ${svc.environmentName}`, svc);
    emit({ type: "domain.updated", serviceId, projectId: svc.projectId });
  },

  async generateDomain(serviceId: string) {
    const svc = serviceById(serviceId);
    const base = svc.environmentName === "production" ? "app" : "dev";
    let name = `${svc.name}-${svc.projectId}-${svc.environmentName}.nordtempo.${base}`;
    if (domains.some((d) => d.hostname === name)) {
      name = name.replace(".", `-${hex(stamp(), 4)}.`);
    }
    domains.push(domain(serviceId, name, wildcard, true));
    record(`generate domain ${name} for ${svc.name}`, svc);
    emit({ type: "domain.updated", serviceId, projectId: svc.projectId });
  },

  async removeDomain(domainId: string) {
    const index = domains.findIndex((d) => d.id === domainId);
    const [removed] = index >= 0 ? domains.splice(index, 1) : [];
    if (!removed) throw new NotFoundError("Domain not found");
    const svc = serviceById(removed.serviceId);
    record(`remove domain ${removed.hostname} from ${svc.name}`, svc);
    emit({
      type: "domain.updated",
      serviceId: svc.id,
      projectId: svc.projectId,
    });
  },

  async archiveService(serviceId: string) {
    const svc = serviceById(serviceId);
    services = services.filter((s) => s.id !== serviceId);
    variables = variables.filter((v) => v.serviceId !== serviceId);
    record(`archive ${svc.name} ${svc.environmentName}`, svc);
    emit({ type: "service.updated", serviceId, projectId: svc.projectId });
  },

  async pruneBuildCache(serverId: string) {
    const server = serverById(serverId);
    server.disk.usedGb -= server.disk.prunableGb;
    server.disk.prunableGb = 0;
    auditLog.unshift(
      audit(0, saile, `prune build cache on ${server.name}`, null, null),
    );
    emit({ type: "server.updated", serverId });
  },

  async createToken(
    input: NewToken,
  ): Promise<{ token: Token; secret: string }> {
    if (!input.name.trim()) throw new Error("Name is required");
    if (tokens.some((t) => t.name === input.name)) {
      throw new Error(`A token named ${input.name} already exists`);
    }
    const token: Token = {
      id: `tok_${hex(stamp(), 6)}`,
      name: input.name.trim(),
      scope: input.scope,
      limits: input.limits,
      createdBy: "saile",
      createdAt: stamp(),
      lastUsedAt: null,
      expiresAt:
        input.expiresInDays === null
          ? null
          : new Date(Date.now() + input.expiresInDays * DAY).toISOString(),
    };
    tokens = [token, ...tokens];
    auditLog.unshift(audit(0, saile, `create token ${token.name}`, null, null));
    return {
      token,
      secret: `dock_${hex(token.id, 8)}${hex(token.name, 8)}${hex(stamp(), 8)}`,
    };
  },

  async revokeToken(tokenId: string) {
    const token = tokens.find((t) => t.id === tokenId);
    if (!token) throw new NotFoundError("Token not found");
    tokens = tokens.filter((t) => t.id !== tokenId);
    auditLog.unshift(audit(0, saile, `revoke token ${token.name}`, null, null));
  },
};

function toVariable({ environmentId: _, serviceId: __, ...v }: StoredVariable) {
  return v;
}

function assertIdle(svc: Service) {
  if (svc.state.state === "deploying") {
    throw new Error(
      `${svc.name} is deploying r${svc.state.release}. One deploy at a time per service.`,
    );
  }
}

function record(summary: string, svc: Service) {
  auditLog.unshift(audit(0, saile, summary, svc.name, svc.id));
}

const settingLabels: Record<keyof EditableSettings, string> = {
  port: "port",
  healthCheckPath: "health check path",
  cpuLimit: "cpu limit",
  memoryLimitMb: "memory limit",
};

function changeSummary(change: PendingChange) {
  switch (change.kind) {
    case "variable.set":
      return change.before?.kind === "plain" && !change.secret
        ? `${change.name} ${change.before.value} → ${change.value}`
        : `set ${change.name}`;
    case "variable.delete":
      return `remove ${change.name}`;
    case "setting":
      return `${settingLabels[change.key]} ${change.before} → ${change.after}`;
  }
}

function commitChange(environmentId: string, change: PendingChange) {
  switch (change.kind) {
    case "variable.set": {
      const id = `${change.serviceId ?? environmentId}:${change.name}`;
      variables = variables.filter((v) => v.id !== id);
      if (change.secret) secretValues.set(id, change.value);
      const value: VariableValue = change.secret
        ? secret
        : change.value.includes("${{")
          ? ref(change.value, resolveReferences(environmentId, change.value))
          : plain(change.value);
      variables.push(
        variable(environmentId, change.serviceId, change.name, value),
      );
      return;
    }
    case "variable.delete": {
      const id = `${change.serviceId ?? environmentId}:${change.name}`;
      variables = variables.filter((v) => v.id !== id);
      return;
    }
    case "setting":
      applySetting(change);
  }
}

function applySetting(change: SettingChange) {
  if (!change.serviceId) return;
  const svc = serviceById(change.serviceId);
  svc.settings = { ...svc.settings, [change.key]: change.after };
}

// Resolves ${{service.VAR}} and ${{service.DOMAIN}} within an environment.
function resolveReferences(environmentId: string, template: string) {
  return template.replace(
    /\$\{\{\s*([\w-]+)\.(\w+)\s*\}\}/g,
    (match, svcName, key) => {
      const target = services.find(
        (s) => s.environmentId === environmentId && s.name === svcName,
      );
      if (!target) return match;
      if (key === "DOMAIN")
        return withDomainSummary(target).domains.primary ?? match;
      const found = variables.find(
        (v) => v.serviceId === target.id && v.name === key,
      );
      if (!found) return match;
      if (found.value.kind === "plain") return found.value.value;
      if (found.value.kind === "reference") return found.value.resolved;
      return "•••";
    },
  );
}

// Deploy simulation: walks a release through its steps on timers and emits
// the same events the daemon would.

function startRelease(
  svc: Service,
  change: ReleaseChange,
  build: boolean,
): Release {
  const number =
    Math.max(
      0,
      ...releases.filter((r) => r.serviceId === svc.id).map((r) => r.number),
    ) + 1;
  const previous = svc.activeRelease?.number;
  const busy = svc.state.state === "deploying";
  const created: Release = {
    ...release(
      svc.id,
      number,
      {
        state: "in_progress",
        steps: [
          {
            name: "build",
            state: "pending",
            durationMs: null,
            detail: build ? "build from source" : "reuse image, no build",
          },
          {
            name: "start",
            state: "pending",
            durationMs: null,
            detail: `next to r${previous} on ${svc.environmentId}`,
          },
          {
            name: "health_check",
            state: "pending",
            durationMs: null,
            detail: svc.settings.healthCheckPath
              ? `GET :${svc.settings.port}${svc.settings.healthCheckPath}`
              : `TCP :${svc.settings.port}`,
          },
          {
            name: "cutover",
            state: "pending",
            durationMs: null,
            detail: `route traffic to r${number}, drain r${previous} 10s`,
          },
        ],
      },
      change,
      saile,
      0,
    ),
    createdAt: stamp(),
  };
  releases.unshift(created);
  record(`deploy ${svc.name} ${svc.environmentName} r${number}`, svc);
  // A queued release waits behind the one in flight; fixtures never start it.
  if (!busy) {
    svc.state = { state: "deploying", release: number };
    advance(svc, created, 0);
  }
  notify(svc);
  return structuredClone(created);
}

const stepMs = [900, 500, 1200, 400];

function advance(svc: Service, rel: Release, index: number) {
  if (rel.status.state !== "in_progress") return;
  const steps = rel.status.steps;
  const step = steps[index];
  if (!step) {
    finish(svc, rel);
    return;
  }
  step.state = "running";
  step.durationMs = 0;
  notify(svc);
  pushLog(
    svc.id,
    logLine(
      0,
      rel.number,
      "info",
      `${step.name.replace("_", " ")}: ${step.detail}`,
      index === 0 ? "build" : "app",
    ),
  );
  setTimeout(() => {
    step.state = "done";
    step.durationMs = stepMs[index] ?? 500;
    advance(svc, rel, index + 1);
  }, stepMs[index]);
}

function finish(svc: Service, rel: Release) {
  for (const r of releases) {
    if (r.serviceId === svc.id && r.status.state === "active") {
      r.status = { state: "superseded", imagePruned: false };
    }
  }
  rel.status = { state: "active" };
  svc.activeRelease = { number: rel.number, change: rel.change };
  svc.state = { state: "active" };
  svc.updatedAt = stamp();
  pushLog(
    svc.id,
    logLine(0, rel.number, "info", `r${rel.number} is serving traffic`),
  );
  notify(svc);
}

function notify(svc: Service) {
  emit({
    type: "service.updated",
    serviceId: svc.id,
    projectId: svc.projectId,
  });
}
