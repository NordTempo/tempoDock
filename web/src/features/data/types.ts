import type { components, operations } from "../../api/schema";

// Database panel types (ADR 0016): the generated API schemas, except where
// rows are concerned. The API types a row as json.RawMessage (`unknown`);
// DataCell and DataRow say what the server actually encodes, and the types
// that carry rows use them.

type Schemas = components["schemas"];

export type DataPermission = Schemas["DataPermission"];
export type DataPermissionStep = Schemas["DataPermissionStep"];
export type DataLevel = DataPermission["level"];
export type DataSensitivity = DataPermission["sensitive"];
export type DataPolicyRule = Schemas["DataPolicyRule"];
export type DataSubject = DataPolicyRule["subject"];
export type DataEnvironmentKind = DataPolicyRule["kind"];
export type DataPolicyScope = NonNullable<DataPolicyRule["from"]>;
export type DataPolicy = Schemas["DataPolicy"];
export type UpdateDataPolicyRequest = Schemas["UpdateDataPolicyRequest"];

/** A token's own data access; null follows the policy's tokens rule. */
export type DataTokenAccess = Schemas["DataTokenAccess"];
/** PATCH /tokens/{id}, the same in Dock and tempoBase. */
export type UpdateTokenRequest = Schemas["UpdateTokenRequest"];
/** GET /audit/{id}/detail: the SQL behind a data.* event. */
export type AuditDetail = Schemas["AuditDetail"];

/** GET /data */
export type DataInfo = Schemas["DataInfo"];
export type DataStatus = DataInfo["status"];

/** GET /data/schema */
export type DataSchema = Schemas["DataSchema"];
/** The keys endpoints' query parameters (they have no body schema). */
export type DataKeysRequest = Omit<
  NonNullable<operations["get-data-keys"]["parameters"]["query"]>,
  "org"
>;
export type DataKeyPage = Schemas["DataKeyPage"];
export type DataKeyInfo = Schemas["DataKeyInfo"];
export type DataKeyRequest = Omit<
  operations["get-data-key"]["parameters"]["query"],
  "org"
>;
export type DataKeyValue = Schemas["DataKeyValue"];
export type DataKeyChange = Schemas["DataKeyChange"];
export type DataTable = Schemas["DataTable"];
export type DataColumn = Schemas["DataColumn"];
export type DataForeignKey = Schemas["DataForeignKey"];
export type DataIndex = Schemas["DataIndex"];
export type DataConstraint = Schemas["DataConstraint"];

export type DataFilter = Schemas["DataFilter"];
export type DataFilterOp = DataFilter["op"];
export type DataResultColumn = Schemas["DataResultColumn"];

/**
 * One cell of a DataRow: null, bool, number (small integers and floats),
 * text in the database's output format (everything else, including 64-bit
 * integers, decimals, JSON and binary values as \x hex), a truncated long
 * value, or a masked one.
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

/** GET /data/tables/{table}/rows */
export type DataPage = Omit<Schemas["DataPage"], "rows"> & {
  rows: DataRow[] | null;
};

/** The rows request as the panel builds it; filters go over HTTP as JSON. */
export type DataPageRequest = {
  table: string;
  order?: string;
  filters?: DataFilter[] | null;
  /** MongoDB: a find filter, Extended JSON as typed in the shell. */
  where?: string;
  after?: string;
  limit?: number;
  count?: boolean;
  full?: boolean;
  /** Show sensitive columns (sensitive visible only; audited). */
  reveal?: boolean;
};

export type DataColumnRef = Schemas["DataColumnRef"];

/** GET /data/tables/{table}/references (key goes over HTTP as JSON). */
export type DataReferencesRequest = {
  table: string;
  key: Record<string, string>;
  limit?: number;
};

export type DataReferences = Omit<Schemas["DataReferences"], "page"> & {
  page: DataPage;
};

/** POST /data/query */
export type DataQueryRequest = Schemas["DataQueryRequest"];
export type DataQueryError = Schemas["DataQueryError"];

/** One NDJSON line of POST /data/query: header, rows..., then end or error. */
export type DataQueryFrame = Omit<Schemas["DataQueryFrame"], "rows"> & {
  rows?: DataRow[] | null;
};

export type DataChange = Schemas["DataChange"];
export type DataChangesetRequest = Schemas["DataChangesetRequest"];
export type DataChangeset = Schemas["DataChangeset"];
export type DataConflict = Schemas["DataConflict"];
export type DataCommitRequest = Schemas["DataCommitRequest"];
export type DataRejectRequest = Schemas["DataRejectRequest"];

export type DataSavedQuery = Schemas["DataSavedQuery"];
export type SaveDataQueryRequest = Schemas["SaveDataQueryRequest"];

/** One of the caller's past queries, from their data.query audit events. */
export type DataHistoryEntry = Schemas["DataHistoryEntry"];

/** Limits from internal/apitypes/data.go the UI needs to know. */
export const dataLimits = {
  pageRows: 100,
  queryRows: 10_000,
  cellBytes: 8 << 10,
  changesetRows: 500,
  // Query history entries per page (the server's default too).
  historyRows: 50,
} as const;

/** GET /data/insights: how the database is doing (ADR 0069). */
export type DataInsights = Schemas["DataInsights"];
export type DataInsightView = NonNullable<Schemas["DataUnavailable"]["view"]>;
export type DataStatementOrder = "total" | "mean" | "calls" | "rows";
export type DataOverview = Schemas["DataOverview"];
export type DataPressure = Schemas["DataPressure"];
export type DataSession = Schemas["DataSession"];
export type DataStatement = Schemas["DataStatement"];
export type DataTableStats = Schemas["DataTableStats"];
export type DataIndexStats = Schemas["DataIndexStats"];
export type DataKeyPrefix = Schemas["DataKeyPrefix"];
export type DataHint = Schemas["DataHint"];
export type DataCancelRequest = Schemas["DataCancelRequest"];
export type DataCancelResult = Schemas["DataCancelResult"];
export type DataQueryStatsResult = Schemas["DataQueryStatsResult"];
