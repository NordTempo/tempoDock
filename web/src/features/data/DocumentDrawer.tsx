import { useState } from "react";
import { useKeydown } from "../../shell/keys";
import { bsonText } from "./bson";
import type { DataResultColumn, DataRow, DataTable } from "./types";

// The open MongoDB document (ADR 0110, mock A): its fields as a tree, each
// with its value as the shell shows it and its BSON type; documents and
// arrays open in place. Esc closes. Edits are the grid's, like any table's.

type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

const wrapperTypes: Record<string, string> = {
  $oid: "ObjectId",
  $date: "date",
  $numberInt: "int",
  $numberLong: "long",
  $numberDouble: "double",
  $numberDecimal: "decimal",
  $binary: "binData",
  $regularExpression: "regex",
  $timestamp: "timestamp",
  $masked: "masked",
  $minKey: "minKey",
  $maxKey: "maxKey",
  $code: "code",
  $symbol: "symbol",
  $undefined: "undefined",
};

/** A value's children when it is a document or an array (not a wrapper). */
function children(v: Json): [string, Json][] | null {
  if (Array.isArray(v)) return v.map((e, i) => [String(i), e]);
  if (v === null || typeof v !== "object") return null;
  const keys = Object.keys(v);
  if (keys.length === 1 && keys[0]! in wrapperTypes) return null;
  return keys.map((k) => [k, v[k] ?? null]);
}

function typeOf(v: Json): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  if (typeof v !== "object") return typeof v === "boolean" ? "bool" : typeof v;
  const keys = Object.keys(v);
  return (keys.length === 1 && wrapperTypes[keys[0]!]) || "object";
}

function Field({
  name,
  value,
  depth,
}: {
  name: string;
  value: Json;
  depth: number;
}) {
  const kids = children(value);
  const [open, setOpen] = useState(depth === 0 && (kids?.length ?? 0) <= 8);
  const type = typeOf(value);
  const text = bsonText(JSON.stringify(value));
  const muted = type === "null" || type === "masked";
  const row = (
    <>
      <span className="w-3 shrink-0 text-(--g-faint)">
        {kids && kids.length > 0 ? (open ? "▾" : "▸") : ""}
      </span>
      <span className="shrink-0 text-(--g-text)">{name}</span>
      <span
        className={`min-w-0 flex-1 truncate ${muted ? "text-(--g-faint)" : type === "string" ? "text-(--g-add)" : "text-(--g-code)"}`}
        title={text}
      >
        {kids && open ? "" : text}
      </span>
      <span className="shrink-0 font-sans text-2xs text-(--g-faint)">
        {type}
      </span>
    </>
  );
  return (
    <>
      {kids && kids.length > 0 ? (
        <button
          type="button"
          className="flex w-full items-center gap-2 py-0.5 text-left hover:bg-(--g-hover)"
          style={{ paddingLeft: depth * 14 }}
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {row}
        </button>
      ) : (
        <div
          className="flex items-center gap-2 py-0.5"
          style={{ paddingLeft: depth * 14 }}
        >
          {row}
        </div>
      )}
      {open &&
        kids?.map(([k, v]) => (
          <Field key={k} name={k} value={v} depth={depth + 1} />
        ))}
    </>
  );
}

/** The selected document of a MongoDB collection. */
export function DocumentDrawer({
  table,
  columns,
  row,
  onClose,
}: {
  table: DataTable;
  columns: readonly DataResultColumn[];
  row: DataRow;
  onClose: () => void;
}) {
  useKeydown((e) => e.key === "Escape" && onClose());
  const parse = (cell: unknown): Json | undefined => {
    if (typeof cell !== "string") return undefined;
    try {
      return JSON.parse(cell) as Json;
    } catch {
      return cell;
    }
  };
  const id = parse(row[0]);
  const idText = id === undefined ? "" : bsonText(JSON.stringify(id));
  return (
    <aside
      aria-label="Document"
      className="g-aside w-[360px] shrink-0 basis-[360px]"
    >
      <div className="flex h-7 items-center justify-between text-xs text-(--g-mut)">
        <span>Document</span>
        <button
          type="button"
          aria-label="Close the document"
          className="g-btn ghost sm"
          onClick={onClose}
        >
          <span className="g-kbd">Esc</span>
        </button>
      </div>
      <h2
        className="mt-1 mb-3 truncate text-[15px] font-semibold"
        title={idText}
      >
        {table.name}
        <span className="font-mono text-xs font-normal text-(--g-dim)">
          {" "}
          · {idText}
        </span>
      </h2>
      <div className="flex flex-col font-mono text-xs leading-5">
        {columns.map((c, i) => {
          const cell = row[i] ?? null;
          if (cell === null) return null; // the document has no such field
          if (typeof cell === "object") {
            return (
              <div key={c.name} className="flex items-center gap-2 py-0.5">
                <span className="w-3 shrink-0" />
                <span className="text-(--g-text)">{c.name}</span>
                <span className="flex-1 text-(--g-faint)">
                  {"masked" in cell ? "••••••" : `${cell.truncated}…`}
                </span>
              </div>
            );
          }
          const v = parse(cell);
          return v === undefined ? null : (
            <Field key={c.name} name={c.name} value={v} depth={0} />
          );
        })}
      </div>
    </aside>
  );
}
