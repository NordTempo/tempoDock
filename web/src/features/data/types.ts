// Database panel types (ADR 0016), mirroring internal/apitypes/data.go field
// for field with the generator's conventions: omitempty fields are optional,
// Go slices are `T[] | null`, times are ISO strings.
//
// Phase 3: once the Surface track registers the data routes, `make generate`
// emits these as components["schemas"]["Data..."]. Replace each declaration
// here with `Schemas["<same name>"]` (the names match) and keep DataCell and
// DataRow, which are more precise than the generated json.RawMessage.

export type DataLevel = "none" | "schema" | "read" | "propose" | "write";
export type DataSensitivity = "masked" | "visible";
export type DataSubject = "role:read" | "role:deploy" | "tokens";
export type DataPolicyScope =
  | "built_in"
  | "default"
  | "project"
  | "environment";

export type DataPermissionStep = {
  source: "admin" | "policy" | "token" | "cap" | "production";
  policy?: DataPolicyScope;
  subject?: DataSubject;
  level: DataLevel;
  sensitive: DataSensitivity;
  detail: string;
};

export type DataPermission = {
  level: DataLevel;
  sensitive: DataSensitivity;
  approve: boolean;
  manage: boolean;
  confirm_commit: boolean;
  why: DataPermissionStep[] | null;
};

export type DataStatus =
  | "ready"
  | "setup_needed"
  | "unsupported"
  | "unreachable"
  | "not_running";

/** GET /data */
export type DataInfo = {
  engine: "postgres";
  detected_by: "setting" | "image";
  status: DataStatus;
  reason?: string;
  server_version?: string;
  set_up_at?: string;
  set_up_by?: string;
  used_by: string[] | null;
  access: DataPermission;
};

export type DataColumn = {
  name: string;
  type: string;
  type_name: string;
  nullable: boolean;
  default?: string;
  generated: boolean;
  sensitive: boolean;
  comment?: string;
};

export type DataForeignKey = {
  name: string;
  columns: string[] | null;
  ref_schema: string;
  ref_table: string;
  ref_columns: string[] | null;
};

export type DataIndex = {
  name: string;
  unique: boolean;
  primary: boolean;
  definition: string;
};

export type DataConstraint = {
  name: string;
  kind: "check" | "unique" | "exclusion";
  definition: string;
};

export type DataTable = {
  schema: string;
  name: string;
  kind:
    | "table"
    | "partitioned_table"
    | "view"
    | "materialized_view"
    | "foreign_table";
  /** -1 when the table was never analysed. */
  estimated_rows: number;
  size_bytes: number;
  comment?: string;
  columns: DataColumn[] | null;
  primary_key: string[] | null;
  foreign_keys: DataForeignKey[] | null;
  indexes: DataIndex[] | null;
  constraints: DataConstraint[] | null;
  editable: boolean;
};

/** GET /data/schema */
export type DataSchema = {
  tables: DataTable[] | null;
  read_at: string;
};

export type DataFilterOp =
  | "eq"
  | "ne"
  | "lt"
  | "lte"
  | "gt"
  | "gte"
  | "like"
  | "ilike"
  | "in"
  | "is_null"
  | "not_null";

export type DataFilter = {
  column: string;
  op: DataFilterOp;
  value?: string;
  values?: string[] | null;
};

/** GET /data/tables/{table}/rows (query parameters over HTTP). */
export type DataPageRequest = {
  table: string;
  order?: string;
  filters?: DataFilter[] | null;
  after?: string;
  limit?: number;
  count?: boolean;
  full?: boolean;
};

export type DataResultColumn = { name: string; type_name: string };

/**
 * One cell of a DataRow: null, bool, number (int2/int4/oid/float4/float8),
 * text in Postgres output format (everything else, including int8, numeric,
 * json and bytea as \x hex), a truncated long value, or a masked one.
 */
export type DataCell =
  | null
  | boolean
  | number
  | string
  | { truncated: string; bytes: number }
  | { masked: true };

/** One result row, one cell per column in column order. */
export type DataRow = DataCell[];

export type DataPage = {
  columns: DataResultColumn[] | null;
  rows: DataRow[] | null;
  next?: string;
  estimated_rows: number;
  exact_rows?: number;
};

/** GET /data/tables/{table}/references (key is a JSON query parameter). */
export type DataReferencesRequest = {
  table: string;
  key: Record<string, string>;
  limit?: number;
};

export type DataReferences = {
  constraint: string;
  schema: string;
  table: string;
  columns: string[] | null;
  filters: DataFilter[] | null;
  page: DataPage;
};

/** POST /data/query */
export type DataQueryRequest = {
  sql: string;
  limit?: number;
  timeout_seconds?: number;
};

export type DataQueryError = {
  message: string;
  sqlstate?: string;
  detail?: string;
  hint?: string;
  /** 1-based character in the SQL, 0 or absent when none. */
  position?: number;
};

/** One NDJSON line of POST /data/query: header, rows..., then end or error. */
export type DataQueryFrame = {
  type: "header" | "rows" | "end" | "error";
  columns?: DataResultColumn[] | null;
  rows?: DataRow[] | null;
  row_count?: number;
  truncated?: boolean;
  command?: string;
  duration_ms?: number;
  error?: DataQueryError;
};

export type DataChange = {
  op: "insert" | "update" | "delete";
  table: string;
  key?: Record<string, string>;
  old?: Record<string, string | null>;
  new?: Record<string, string | null>;
};

/** POST /data/changesets */
export type DataChangesetRequest = {
  changes: DataChange[] | null;
  message?: string;
  dry_run?: boolean;
};

export type DataConflict = {
  change: number;
  reason: "changed" | "missing" | "failed";
  current?: Record<string, string | null>;
  message: string;
};

export type DataChangeset = {
  id: number;
  status:
    | "preview"
    | "pending"
    | "committed"
    | "rejected"
    | "expired"
    | "conflict";
  author: string;
  author_kind: "user" | "token";
  message?: string;
  changes: DataChange[] | null;
  sql: string;
  rows_affected?: number[] | null;
  conflicts?: DataConflict[] | null;
  confirm_commit: boolean;
  created_at: string;
  expires_at?: string;
  decided_at?: string;
  decided_by?: string;
  reason?: string;
};

export type DataSavedQuery = {
  id: number;
  name: string;
  sql: string;
  created_at: string;
  updated_at: string;
};

export type SaveDataQueryRequest = { name: string; sql: string };

/** One of the caller's past queries, from their data.query audit events. */
export type DataHistoryEntry = {
  audit_id: number;
  created_at: string;
  id: number;
  sql: string;
  rows: number;
  duration_ms: number;
  outcome: "ok" | "error" | "timeout" | "canceled" | "conflict";
};

/** Limits from internal/apitypes/data.go the UI needs to know. */
export const dataLimits = {
  pageRows: 100,
  queryRows: 10_000,
  cellBytes: 8 << 10,
  changesetRows: 500,
} as const;
