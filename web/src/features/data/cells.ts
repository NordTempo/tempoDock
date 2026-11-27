import { csv } from "../../ui/csv";
import { bsonDetail, bsonText, isBson, isBsonNumber } from "./bson";
import type {
  DataCell,
  DataFilter,
  DataResultColumn,
  DataRow,
  DataTable,
} from "./types";

// Reading DataRow cells (see DataCell) for display, keys and filters.

/** How a cell is shown: each kind has its own look in the grid. */
export type CellKind =
  | "null"
  | "masked"
  | "truncated"
  | "bool"
  | "number"
  | "json"
  | "bytea"
  | "text";

const numericTypes = new Set([
  "int2",
  "int4",
  "int8",
  "float4",
  "float8",
  "numeric",
  "oid",
  "money",
]);

/** Right-aligned in grids: numbers, including int8 and numeric sent as text. */
export const isNumericType = (typeName: string) =>
  numericTypes.has(typeName) || isBsonNumber(typeName);

export function cellKind(cell: DataCell, typeName: string): CellKind {
  if (cell === null) return "null";
  if (typeof cell === "object")
    return "masked" in cell ? "masked" : "truncated";
  if (typeof cell === "boolean") return "bool";
  if (typeof cell === "number") return "number";
  if (isBson(typeName))
    return typeName === "bson:object" || typeName === "bson:array"
      ? "json"
      : isBsonNumber(typeName)
        ? "number"
        : "text";
  if (typeName === "json" || typeName === "jsonb") return "json";
  if (typeName === "bytea") return "bytea";
  return isNumericType(typeName) ? "number" : "text";
}

/** "12 KB", "3.4 MB" */
export function byteSize(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

/**
 * A timestamp in UTC or without a zone, as Postgres' ISO text
 * ("2026-01-01 07:00:00.25+00") or JSON's ("2026-01-01T07:00:00.000Z").
 */
const isoTime =
  /^(\d{4}-\d\d-\d\d)[T ](\d\d:\d\d:\d\d)(?:\.\d+)?(?:Z|[+-]00(?::?00)?)?$/;

/**
 * One line of text for a cell in a grid row. Timestamps show to the
 * second (the grid's header says UTC); the row details have them whole.
 */
export function cellText(cell: DataCell, typeName: string): string {
  // A MongoDB document without the field.
  if (cell === null) return isBson(typeName) ? "" : "NULL";
  if (typeof cell === "object") {
    return "masked" in cell
      ? "••••••"
      : `${oneLine(cell.truncated)}… ${byteSize(cell.bytes)}`;
  }
  if (typeof cell !== "string") return String(cell);
  if (isBson(typeName)) return bsonText(cell);
  if (typeName === "timestamptz" || typeName === "timestamp") {
    const m = isoTime.exec(cell);
    if (m) return `${m[1]} ${m[2]}`;
  }
  if (typeName === "bytea" && cell.length > 34) {
    return `${cell.slice(0, 34)}… ${byteSize((cell.length - 2) / 2)}`;
  }
  return oneLine(cell);
}

/** The whole value, for the row detail: JSON pretty-printed. */
export function cellDetail(cell: DataCell, typeName: string): string {
  if (typeof cell === "string" && isBson(typeName)) return bsonDetail(cell);
  if (cell === null && isBson(typeName)) return "(no such field)";
  if (
    typeof cell === "string" &&
    (typeName === "json" || typeName === "jsonb")
  ) {
    try {
      return JSON.stringify(JSON.parse(cell), null, 2);
    } catch {
      return cell;
    }
  }
  if (cell !== null && typeof cell === "object" && "truncated" in cell) {
    return `${cell.truncated}\n… ${byteSize(cell.bytes)} in total`;
  }
  return cell === null
    ? "NULL"
    : typeof cell === "object"
      ? "••••••"
      : String(cell);
}

const oneLine = (s: string) => s.replace(/\s*\n\s*/g, " ");

/**
 * A result as CSV: NULL is an empty field, truncated values end in "…",
 * masked ones read "(masked)". It is for opening in a spreadsheet, so a
 * value that one would run as a formula is marked as text (ui/csv.ts);
 * `dock db query --csv` writes values as they are, for scripts.
 */
export function toCsv(columns: DataResultColumn[], rows: DataRow[]) {
  const text = (cell: DataCell) =>
    cell === null
      ? ""
      : typeof cell === "object"
        ? "masked" in cell
          ? "(masked)"
          : `${cell.truncated}…`
        : String(cell);
  return csv([columns.map((c) => c.name), ...rows.map((r) => r.map(text))]);
}

/**
 * A cell as a value sent back to the server (keys, filters, changes): text
 * in Postgres input format, or null. Undefined when the cell isn't whole
 * (truncated or masked), so it can't be used.
 */
export function cellInput(cell: DataCell): string | null | undefined {
  if (cell === null) return null;
  if (typeof cell === "object") return undefined;
  return String(cell);
}

/** "public.orders" */
export const tableName = (t: { schema: string; name: string }) =>
  `${t.schema}.${t.name}`;

/**
 * A row count: exact (counted) or Postgres' estimate; estimated -1 and no
 * exact count means unknown (never analysed, too slow to count).
 */
export type RowCount = { estimated: number; exact?: number | null };

/** Row counts in lists: "~12k" estimated, "830" exact, "?" unknown. */
export function compactCount({ estimated, exact }: RowCount) {
  const n = exact ?? estimated;
  if (n < 0) return "?";
  const text =
    n < 1000
      ? String(n)
      : n < 1_000_000
        ? `${Math.round(n / 1000)}k`
        : `${(n / 1_000_000).toFixed(1)}M`;
  return exact == null ? `~${text}` : text;
}

/** "~12 431 rows" estimated, "830 rows" exact, "rows unknown"; noun
 * names them ("document" on MongoDB). */
export function rowCount({ estimated, exact }: RowCount, noun = "row") {
  const n = exact ?? estimated;
  if (n < 0) return `${noun}s unknown`;
  return `${exact == null ? "~" : ""}${n.toLocaleString("en").replaceAll(",", " ")} ${n === 1 ? noun : `${noun}s`}`;
}

/** A table's row count from the schema. */
export const tableRows = (t: DataTable): RowCount => ({
  estimated: t.estimated_rows,
  exact: t.exact_rows,
});

/** The row's primary key, column to value, or null if it can't be read. */
export function rowKey(
  table: DataTable,
  columns: readonly string[],
  row: readonly DataCell[],
): Record<string, string> | null {
  const pk = table.primary_key ?? [];
  if (!pk.length) return null;
  const key: Record<string, string> = {};
  for (const name of pk) {
    const value = cellInput(row[columns.indexOf(name)] ?? null);
    if (value === null || value === undefined) return null;
    key[name] = value;
  }
  return key;
}

/** Filter operators as people read them. */
export const opLabels: Record<DataFilter["op"], string> = {
  eq: "=",
  ne: "≠",
  lt: "<",
  lte: "≤",
  gt: ">",
  gte: "≥",
  like: "like",
  ilike: "ilike",
  in: "in",
  is_null: "is null",
  not_null: "is not null",
};

/** "status = paid" */
export function filterText(f: DataFilter) {
  if (f.op === "is_null" || f.op === "not_null") {
    return `${f.column} ${opLabels[f.op]}`;
  }
  const value = f.op === "in" ? (f.values ?? []).join(", ") : (f.value ?? "");
  return `${f.column} ${opLabels[f.op]} ${value}`;
}

/** `dock db rows --filter` for a filter: col=value, col=op:value, col=is_null. */
export function filterFlag(f: DataFilter) {
  const rhs =
    f.op === "is_null" || f.op === "not_null"
      ? f.op
      : f.op === "in"
        ? `in:${(f.values ?? []).join(",")}`
        : f.op === "eq"
          ? (f.value ?? "")
          : `${f.op}:${f.value ?? ""}`;
  const arg = `${f.column}=${rhs}`;
  return `--filter ${/^[\w.:,@=%+-]*$/.test(arg) ? arg : `'${arg.replaceAll("'", "'\\''")}'`}`;
}
