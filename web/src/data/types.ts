import type { components } from "../api/schema";
import type { Server } from "./placeholders";

// The UI speaks the API's words: most types are the generated API schemas.
// Where the API returns `null` for empty lists (Go nil slices), the hooks in
// ./queries.ts normalize to arrays, and the types below say so.

type Schemas = components["schemas"];

export type Scope = Schemas["Me"]["scope"];
export type Session = Schemas["Me"];
export type Project = Schemas["Project"];
export type {
  AddedServer,
  NewServer,
  Promotion,
  Server,
  ServerPatch,
} from "./placeholders";
export type ServerStatus = Server["status"];
export type ServerUsage = Schemas["ServerUsage"];
export type ServiceUsage = Schemas["ServiceUsage"];
export type Placement = Schemas["Placement"];
export type EnvironmentKind = Schemas["Environment"]["kind"];
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
export type Variable = Schemas["Variable"];
export type VariableChange = Schemas["VariableChange"];
export type SettingChange = Schemas["SettingChange"];
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

/** An environment, addressed by name like every API path. */
export type EnvRef = { project: string; environment: string };
/** A service, addressed by name. Route params have exactly this shape. */
export type ServiceRef = EnvRef & { service: string };

/** A service with its live state, as on the project overview. */
export type ServiceView = Omit<Schemas["ServiceOverview"], "domains"> & {
  domains: Domain[];
};

export type EnvironmentView = Omit<
  Schemas["EnvironmentOverview"],
  "services"
> & { services: ServiceView[] };

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

export type PendingChanges = Omit<
  Schemas["PendingChanges"],
  "variables" | "settings" | "volumes" | "affected"
> & {
  variables: VariableChange[];
  settings: SettingChange[];
  volumes: Schemas["VolumeAddition"][];
  /** Services that get a release on apply, including dependents. */
  affected: string[];
};

export type AuditFilter = {
  project?: string;
  environment?: string;
  service?: string;
  /** Action prefix, like "release." or "domain.add". */
  action?: string;
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
  | { kind: "domain"; service: ServiceView; domain: Domain }
  | { kind: "staged"; environment: EnvironmentView }
  | { kind: "server_offline"; server: Server };
