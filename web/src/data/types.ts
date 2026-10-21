// View-model types for the UI. Shaped by what the screens need, not by the
// wire format: when the generated API client lands, the hook internals in
// ./queries.ts and ./mutations.ts map API responses onto these.

export type Scope = "read" | "deploy" | "admin";
export type Role = "admin" | "member";

/** Who did something: a logged-in user or a named API token. */
export type Actor =
  | { kind: "user"; name: string }
  | { kind: "token"; name: string; scope: Scope; limit: string };

export type Session = { username: string; role: Role };

export type Org = { name: string; tokenCount: number; userCount: number };

export type Server = {
  id: string;
  name: string;
  role: "production" | "non-production";
  builder: boolean;
  address: string;
  status: { state: "online" } | { state: "offline"; since: string };
  cpuPercent: number;
  memory: { usedMb: number; totalMb: number };
  disk: { usedGb: number; totalGb: number; prunableGb: number };
  environmentCount: number;
  version: string;
};

export type ServiceState =
  | { state: "active" }
  | { state: "deploying"; release: number }
  | { state: "crash_loop"; restarts: number; window: string; exit: string }
  | { state: "stopped" };

/** What a release changed: new code, new config, or an old release again. */
export type ReleaseChange =
  | { kind: "commit"; sha: string; message: string }
  | { kind: "image"; ref: string }
  | { kind: "config"; summary: string }
  | { kind: "rollback"; to: number }
  | { kind: "redeploy"; of: number }
  | { kind: "promote"; from: string; release: number };

export type StepName = "build" | "start" | "health_check" | "cutover";

export type DeployStep = {
  name: StepName;
  state: "done" | "running" | "pending" | "failed";
  /** Milliseconds spent, once started. */
  durationMs: number | null;
  detail: string;
};

export type ReleaseStatus =
  | { state: "in_progress"; steps: DeployStep[] }
  | { state: "active" }
  | { state: "failed"; reason: string }
  | { state: "superseded"; imagePruned: boolean };

export type Release = {
  id: string;
  serviceId: string;
  number: number;
  status: ReleaseStatus;
  change: ReleaseChange;
  actor: Actor;
  createdAt: string;
  imageDigest: string | null;
};

export type ServiceSource =
  | { kind: "upload"; dockerfile: string; context: string }
  | { kind: "image"; ref: string }
  | { kind: "git"; repo: string; branch: string };

/** Settings that can be staged and applied as a release. */
export type EditableSettings = {
  port: number;
  healthCheckPath: string;
  cpuLimit: number;
  memoryLimitMb: number;
};

export type ServiceSettings = EditableSettings & {
  healthCheckIntervalS: number;
  healthCheckTimeoutS: number;
  strategy: "rolling" | "recreate";
  restartPolicy: string;
};

export type Volume = { name: string; mountPath: string; sizeMb: number };

export type Service = {
  id: string;
  name: string;
  projectId: string;
  projectName: string;
  environmentId: string;
  environmentName: string;
  server: { id: string; name: string };
  state: ServiceState;
  activeRelease: { number: number; change: ReleaseChange } | null;
  imageDigest: string | null;
  source: ServiceSource;
  settings: ServiceSettings;
  volume: Volume | null;
  /** Hostname shown first, the total count, and how many need attention. */
  domains: { primary: string | null; count: number; attention: number };
  privateAddress: string;
  cpuPercent: number;
  memory: { usedMb: number; peak24hMb: number };
  updatedAt: string;
};

export type Environment = {
  id: string;
  name: string;
  production: boolean;
  server: { id: string; name: string; address: string };
};

export type DnsRecord = {
  type: "A" | "AAAA" | "CNAME";
  name: string;
  value: string;
};

export type DomainStatus =
  | { state: "active"; certificate: string }
  | {
      state: "waiting_dns";
      record: DnsRecord;
      resolvesTo: string | null;
      checkedAt: string;
      since: string;
    }
  | { state: "issuing_certificate" }
  | { state: "error"; message: string };

export type Domain = {
  id: string;
  serviceId: string;
  hostname: string;
  generated: boolean;
  targetPort: number;
  status: DomainStatus;
};

export type VariableValue =
  | { kind: "plain"; value: string }
  | { kind: "secret" }
  /** `template` is what the user wrote, `resolved` what the release gets. */
  | { kind: "reference"; template: string; resolved: string };

export type Variable = {
  id: string;
  name: string;
  scope: "environment" | "service";
  value: VariableValue;
};

/** A variable as shown in a table, with override relationships resolved. */
export type VariableRow = Variable & {
  /** A service variable that replaces an environment variable of the same name. */
  overrides: boolean;
  /** An environment variable replaced by a service variable. */
  overridden: boolean;
};

/** Where variables live: a whole environment, or one service in it. */
export type VariableTarget = {
  environmentId: string;
  serviceId: string | null;
};

type ChangeBase = { id: string; serviceId: string | null };

export type SettingChange = {
  [K in keyof EditableSettings]: ChangeBase & {
    kind: "setting";
    key: K;
    before: EditableSettings[K];
    after: EditableSettings[K];
  };
}[keyof EditableSettings];

/**
 * A staged edit. Edits pile up per environment until applied, which creates
 * a release for every affected service (Railway-style staged changes).
 */
export type PendingChange =
  | (ChangeBase & {
      kind: "variable.set";
      name: string;
      scope: Variable["scope"];
      before: VariableValue | null;
      value: string;
      secret: boolean;
    })
  | (ChangeBase & {
      kind: "variable.delete";
      name: string;
      scope: Variable["scope"];
      before: VariableValue;
    })
  | SettingChange;

/** Omit that keeps a union a union. */
type DistributiveOmit<T, K extends PropertyKey> = T extends unknown
  ? Omit<T, K>
  : never;

/** A change as submitted for staging; the server assigns the id. */
export type NewChange = DistributiveOmit<PendingChange, "id">;

/** Something on the project overview that needs a human. */
export type Problem =
  | {
      kind: "crash_loop";
      service: Service;
      restarts: number;
      window: string;
      exit: string;
    }
  | { kind: "deploy_failed"; service: Service; release: number; reason: string }
  | { kind: "deploying"; service: Service; release: number; step: string }
  | { kind: "dns_waiting"; service: Service; domain: Domain; record: DnsRecord }
  | { kind: "disk"; server: Server };

export type AuditEvent = {
  id: string;
  at: string;
  actor: Actor;
  /** Plain-language summary, e.g. "deploy api production r89". */
  summary: string;
  /** Service name in the summary, rendered emphasized. */
  subject: string | null;
  projectId: string | null;
  serviceId: string | null;
};

export type AuditFilter = { projectId?: string; serviceId?: string };

export type ProjectOverview = {
  project: { id: string; name: string };
  environments: Array<Environment & { services: Service[] }>;
  problems: Problem[];
  recent: AuditEvent[];
};

/** Sidebar tree: every project with its environments and services. */
export type ProjectTree = {
  id: string;
  name: string;
  failing: number;
  warning: number;
  serviceCount: number;
  environments: Array<{
    id: string;
    name: string;
    serverName: string;
    services: Array<{
      id: string;
      name: string;
      state: ServiceState;
      release: number | null;
    }>;
  }>;
};

export type LogLevel = "debug" | "info" | "warn" | "error";

export type LogLine = {
  id: string;
  at: string;
  release: number;
  stream: "app" | "build";
  level: LogLevel;
  message: string;
};

/**
 * Live updates pushed by the daemon over SSE. Log lines carry their payload
 * so the cache can be patched in place; the rest only say what changed.
 */
export type LiveEvent =
  | { type: "log.line"; serviceId: string; line: LogLine }
  | { type: "service.updated"; serviceId: string; projectId: string }
  | { type: "domain.updated"; serviceId: string; projectId: string }
  | { type: "server.updated"; serverId: string };

export type Token = {
  id: string;
  name: string;
  scope: Scope;
  /** Project/environment limits like "shop/*" or "shop/staging". Empty means all. */
  limits: string[];
  createdBy: string;
  createdAt: string;
  lastUsedAt: string | null;
  expiresAt: string | null;
};

export type NewToken = Pick<Token, "name" | "scope" | "limits"> & {
  expiresInDays: number | null;
};

export type User = {
  id: string;
  username: string;
  role: Role;
  createdAt: string;
  lastSeenAt: string | null;
};
