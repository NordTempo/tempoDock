// MongoDB's cells (ADR 0110): each is a field's canonical Extended JSON
// text, in a column typed bson:<type>. These read it for the grid (one
// short line, as the shell shows a value) and the drawer (the whole
// value, indented).

/** Whether a column holds Extended JSON (a MongoDB field). */
export const isBson = (typeName: string) => typeName.startsWith("bson:");

type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

const wrappers = [
  "$numberInt",
  "$numberLong",
  "$numberDouble",
  "$numberDecimal",
] as const;

/** "2026-11-27T09:14:02.000Z" as "2026-11-27 09:14:02". */
const dateText = (iso: string) =>
  iso.replace("T", " ").replace(/\.\d+Z$|Z$/, "");

/** A scalar wrapper's text, or undefined when v isn't one. */
function scalar(v: { [k: string]: Json }): string | undefined {
  const keys = Object.keys(v);
  if (keys.length !== 1) return undefined;
  const [k] = keys;
  const x = v[k as string];
  if (wrappers.some((w) => w === k) && typeof x === "string") return x;
  switch (k) {
    case "$oid":
      return typeof x === "string" ? x : undefined;
    case "$date":
      if (typeof x === "string") return dateText(x);
      if (x && typeof x === "object" && !Array.isArray(x)) {
        const ms = x.$numberLong;
        return typeof ms === "string" ? `Date(${ms})` : undefined;
      }
      return undefined;
    case "$masked":
      return "••••••";
    case "$binary": {
      if (!x || typeof x !== "object" || Array.isArray(x)) return undefined;
      const b64 = typeof x.base64 === "string" ? x.base64 : "";
      if (x.subType === "04") {
        const hex = Array.from(atob(b64), (c) =>
          c.charCodeAt(0).toString(16).padStart(2, "0"),
        ).join("");
        return hex.replace(
          /^(.{8})(.{4})(.{4})(.{4})(.{12})$/,
          "$1-$2-$3-$4-$5",
        );
      }
      return `Binary(${Math.floor((b64.length * 3) / 4)} bytes)`;
    }
    case "$regularExpression":
      if (!x || typeof x !== "object" || Array.isArray(x)) return undefined;
      return `/${String(x.pattern)}/${String(x.options)}`;
    case "$timestamp":
    case "$minKey":
    case "$maxKey":
    case "$undefined":
    case "$code":
    case "$symbol":
      return k.slice(1);
  }
  return undefined;
}

/** One line for a grid cell: a string bare, a number as written, a date
 * to the second, a document as its keys, an array as its length. */
export function bsonText(cell: string): string {
  let v: Json;
  try {
    v = JSON.parse(cell) as Json;
  } catch {
    return cell;
  }
  if (v === null) return "null";
  if (typeof v === "string") return v.replace(/\s*\n\s*/g, " ");
  if (typeof v !== "object") return String(v);
  if (Array.isArray(v)) return `[${v.length}]`;
  const s = scalar(v);
  if (s !== undefined) return s;
  const keys = Object.keys(v);
  return keys.length === 0
    ? "{}"
    : `{ ${keys.slice(0, 6).join(", ")}${keys.length > 6 ? ", …" : ""} }`;
}

/** The whole value as the shell writes it, indented: numbers and IDs
 * without their wrappers, dates as ISODate("..."). */
export function bsonDetail(cell: string): string {
  let v: Json;
  try {
    v = JSON.parse(cell) as Json;
  } catch {
    return cell;
  }
  const out = (x: Json, indent: string): string => {
    if (x === null || typeof x !== "object") return JSON.stringify(x);
    const next = `${indent}  `;
    if (Array.isArray(x)) {
      if (x.length === 0) return "[]";
      return `[\n${x.map((e) => next + out(e, next)).join(",\n")}\n${indent}]`;
    }
    const keys = Object.keys(x);
    const only = keys.length === 1 ? keys[0] : undefined;
    const inner = only === undefined ? undefined : x[only];
    if (only === "$oid") return `ObjectId(${JSON.stringify(inner)})`;
    if (only === "$date" && typeof inner === "string")
      return `ISODate(${JSON.stringify(inner)})`;
    if (only === "$numberDecimal")
      return `Decimal128(${JSON.stringify(inner)})`;
    if (only === "$numberLong") return `NumberLong(${JSON.stringify(inner)})`;
    if (only === "$numberInt" || only === "$numberDouble") return String(inner);
    if (only === "$masked") return "••••••";
    if (keys.length === 0) return "{}";
    return `{\n${keys
      .map((k) => `${next}${JSON.stringify(k)}: ${out(x[k] ?? null, next)}`)
      .join(",\n")}\n${indent}}`;
  };
  return out(v, "");
}

/** Right-aligned in grids. */
export const isBsonNumber = (typeName: string) =>
  ["bson:int", "bson:long", "bson:double", "bson:decimal"].includes(typeName);
