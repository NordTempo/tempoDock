import type { components } from "../api/schema";

// The UI speaks the API's words: most types are the generated API schemas.
// Where the API returns `null` for empty lists (Go nil slices), the hooks in
// ./queries.ts normalize to arrays, and the types below say so.

type Schemas = components["schemas"];

export type Scope = Schemas["User"]["role"];
export type Session = Schemas["Me"];
export type Org = Schemas["Org"];
/** Whether new production environments start locked; request expiry. */
export type OrgLockSettings = Schemas["OrgLockSettings"];
export type Project = Schemas["Project"];
export type Server = Schemas["Server"];
export type ServerStatus = Server["status"];
export type ServerPatch = Schemas["UpdateServerRequest"];
export type NewServer = Schemas["AddServerRequest"];
/** A new server with its join token and install command, shown once. */
export type AddedServer = Schemas["AddedServer"];
export type Promotion = Schemas["PromoteResult"];
export type NewClone = Schemas["CloneEnvironmentRequest"];
export type Clone = Schemas["CloneResult"];
export type SkippedService = Schemas["SkippedService"];
export type ServerUsage = Schemas["ServerUsage"];
export type MemoryPart = Schemas["MemoryPart"];
export type ServiceUsage = Schemas["ServiceUsage"];
export type Placement = Schemas["Placement"];
export type EnvironmentKind = Schemas["Environment"]["kind"];
export type Environment = Schemas["Environment"];
/** An environment's lock (ADR 0044); absent when unlocked. */
export type EnvironmentLock = Schemas["EnvironmentLock"];
/** A token's call a locked environment stopped, waiting for an admin. */
export type AccessRequest = Schemas["AccessRequest"];
/** How far back statistics go: a point a minute, 10 minutes for 7d. */
export type StatsRange = Schemas["ServerStats"]["range"];
export type Service = Schemas["Service"];
export type ServiceStatus = Schemas["ServiceStatus"];
export type Release = Schemas["Release"];
export type ReleaseSummary = Schemas["ReleaseSummary"];
export type ReleaseEvent = Schemas["ReleaseEvent"];
export type Domain = Schemas["Domain"];
export type DnsRecord = Schemas["DNSRecord"];
export type Volume = Schemas["Volume"];
/** A database template: a service with a volume and credentials (ADR 0019). */
export type TemplateInfo = Omit<Schemas["TemplateInfo"], "exports"> & {
  exports: string[];
};
export type BackupRun = Schemas["BackupRun"];
/** How, when and where a stateful service is backed up, with its runs. */
export type ServiceBackups = Omit<Schemas["ServiceBackups"], "runs"> & {
  runs: BackupRun[];
};
export type Variable = Schemas["Variable"];
export type VariableChange = Schemas["VariableChange"];
export type SettingChange = Schemas["SettingChange"];
export type ServiceStaging = Schemas["ServiceStaging"];
export type DomainStaging = Schemas["DomainStaging"];
export type Token = Schemas["Token"];
/** A token's own database access, overriding the data policy (ADR 0016). */
export type TokenDataAccess = NonNullable<Schemas["DataTokenAccess"]>;
export type NewToken = Schemas["CreateTokenRequest"];
export type NewUser = Schemas["CreateUserRequest"];
export type User = Schemas["User"];
export type AuditEvent = Schemas["AuditEvent"];
export type LiveEvent = Schemas["Event"];
export type SettingsPatch = Schemas["UpdateServiceRequest"];
/** Staged changes to discard; an empty selection means all of them. */
export type DiscardSelection = Schemas["DiscardChangesRequest"];
/** What applying staged changes started, and what stays staged. */
export type AppliedChanges = Schemas["AppliedChanges"];
/** A project file to plan or stage (`dock apply FILE`, ADR 0049). */
export type ApplyRequest = Schemas["ApplyRequest"];
export type ApplyPlan = Schemas["ApplyPlan"];
export type PlanChange = Schemas["PlanChange"];
/** A Docker Compose file to convert, plan or create (ADR 0049). */
export type ComposeImport = Schemas["ComposeImport"];
export type ComposeImportRequest = Schemas["ComposeImportRequest"];
export type ComposeSecret = Schemas["ComposeSecret"];

/** An environment, addressed by name like every API path. */
export type EnvRef = { project: string; environment: string };
/** A service, addressed by name. Route params have exactly this shape. */
export type ServiceRef = EnvRef & { service: string };

/** A service with its live state, as on the project overview. */
export type ServiceView = Omit<Schemas["ServiceOverview"], "domains"> & {
  domains: Domain[];
};

/**
 * An environment on the overview. `services` are the live ones that
 * navigation shows; `archived` are kept apart (`dock service list --all`).
 */
export type EnvironmentView = Omit<
  Schemas["EnvironmentOverview"],
  "services"
> & { services: ServiceView[]; archived: ServiceView[] };

export type ProjectOverview = {
  project: Project;
  environments: EnvironmentView[];
};

/** A server with its latest sample and what is placed on it. */
export type ServerDetail = Omit<
  Schemas["ServerDetail"],
  "server" | "placements" | "services"
> & {
  server: Server;
  placements: Schemas["ServerPlacement"][];
  services: ServerService[];
};

/** A service on a server page, with its latest usage. */
export type ServerService = Schemas["ServerService"];

export type ServerStats = Omit<Schemas["ServerStats"], "points"> & {
  points: Schemas["ServerStatsPoint"][];
};

export type ServiceStats = Omit<Schemas["ServiceStats"], "points"> & {
  points: Schemas["ServiceStatsPoint"][];
};

/** How far back traffic goes: a point a minute for 24h, an hour for 7d and 30d. */
export type TrafficRange = Schemas["ServiceTraffic"]["range"];
export type TrafficPoint = Schemas["TrafficPoint"];
export type TrafficPath = Schemas["TrafficPath"];
/** What a service's domains served over a range (ADR 0033). */
export type ServiceTraffic = Omit<
  Schemas["ServiceTraffic"],
  "points" | "paths"
> & {
  points: TrafficPoint[];
  paths: TrafficPath[];
};

export type PendingChanges = Omit<
  Schemas["PendingChanges"],
  "variables" | "settings" | "volumes" | "services" | "domains" | "affected"
> & {
  variables: VariableChange[];
  settings: SettingChange[];
  volumes: Schemas["VolumeAddition"][];
  /** Staged service creations and archives (project files, ADR 0049). */
  services: ServiceStaging[];
  domains: DomainStaging[];
  /** Services that get a release on apply, including dependents. */
  affected: string[];
};

export type AuditFilter = {
  project?: string;
  environment?: string;
  service?: string;
  /** Action prefix, like "release." or "domain.add". */
  action?: string;
  /** How many, newest first: 100 unless set, at most 500. */
  limit?: number;
  /** The platform's events instead of the organization's (operators). */
  platform?: true;
};

/** One line in a log view: container output or build output. */
export type LogLine = {
  key: string;
  /** Build output has no timestamps. */
  time: string | null;
  stream: "stdout" | "stderr" | "build";
  text: string;
};

/** Something on the project overview that needs a human, derived from state. */
export type Problem =
  | { kind: "crashed"; service: ServiceView }
  | { kind: "deploy_failed"; service: ServiceView; release: ReleaseSummary }
  | { kind: "backup_failed"; service: ServiceView; backup: BackupRun }
  | { kind: "domain"; service: ServiceView; domain: Domain }
  | { kind: "staged"; environment: EnvironmentView }
  /** An environment whose server is offline; only operators see which. */
  | { kind: "server_offline"; environment: Environment }
  /** An open alert live state doesn't show (a full disk, a failed backup). */
  | { kind: "alert"; alert: Schemas["Alert"] };
