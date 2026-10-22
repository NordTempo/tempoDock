import type { DataCell, DataFilter, DataTable } from "./types";

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
export const isNumericType = (typeName: string) => numericTypes.has(typeName);

export function cellKind(cell: DataCell, typeName: string): CellKind {
  if (cell === null) return "null";
  if (typeof cell === "object")
    return "masked" in cell ? "masked" : "truncated";
  if (typeof cell === "boolean") return "bool";
  if (typeof cell === "number") return "number";
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

/** One line of text for a cell in a grid row. */
export function cellText(cell: DataCell, typeName: string): string {
  if (cell === null) return "NULL";
  if (typeof cell === "object") {
    return "masked" in cell
      ? "••••••"
      : `${oneLine(cell.truncated)}… ${byteSize(cell.bytes)}`;
  }
  if (typeof cell !== "string") return String(cell);
  if (typeName === "bytea" && cell.length > 34) {
    return `${cell.slice(0, 34)}… ${byteSize((cell.length - 2) / 2)}`;
  }
  return oneLine(cell);
}

/** The whole value, for the row detail: JSON pretty-printed. */
export function cellDetail(cell: DataCell, typeName: string): string {
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

/** Row counts in lists: "830", "12k", "1.2M"; "?" when never analysed. */
export function compactCount(n: number) {
  if (n < 0) return "?";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** "~12 431 rows", "not analysed". */
export function estimate(n: number) {
  if (n < 0) return "not analysed";
  return `~${n.toLocaleString("en").replaceAll(",", " ")} ${n === 1 ? "row" : "rows"}`;
}

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

/** The CLI's --where for a filter, where it has one (equality only). */
export const whereFlag = (f: DataFilter) =>
  f.op === "eq" ? `--where ${f.column}=${f.value ?? ""}` : null;
