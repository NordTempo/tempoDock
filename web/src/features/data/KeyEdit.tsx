import { useEffect, useRef, useState } from "react";
import { Btn } from "./kit";
import type { DataCell, DataKeyChange } from "./types";

// Editing one Redis key (ADR 0072): the drawer stages key changes, each
// with what the editor saw (Old), and reviews them as one changeset.
// Values that were cut or aren't text (\x hex) aren't editable: their
// old value can't be compared, and binary writes aren't supported.

/** A cell's text when it is editable: whole and not hex. */
export function editable(cell: DataCell | undefined): string | undefined {
  if (typeof cell === "number") return String(cell);
  if (typeof cell !== "string" || cell.startsWith("\\x")) return undefined;
  return cell;
}

/** What one value row of a key becomes: its edit and its removal. */
export function rowChanges(
  type: string,
  key: string,
  db: number,
  cells: readonly DataCell[],
): {
  /** The value to edit (undefined: not editable), and its column. */
  current?: string;
  column?: number;
  edit?: (value: string) => DataKeyChange;
  remove?: () => DataKeyChange;
} {
  const old = (value?: string) => ({ type, value });
  const [a, b, c] = cells;
  switch (type) {
    case "string": {
      const current = editable(a);
      return {
        current,
        column: 0,
        edit: (value) => ({ op: "set", db, key, value, old: old(current) }),
      };
    }
    case "hash": {
      const field = editable(a);
      const current = editable(b);
      if (field === undefined) return {};
      return {
        current,
        column: 1,
        edit: (value) => ({
          op: "hset",
          db,
          key,
          field,
          value,
          old: old(current),
        }),
        remove: () => ({ op: "hdel", db, key, field, old: old(current) }),
      };
    }
    case "set": {
      const member = editable(a);
      if (member === undefined) return {};
      return {
        remove: () => ({ op: "srem", db, key, field: member, old: old("1") }),
      };
    }
    case "zset": {
      const member = editable(b);
      const score = editable(c);
      if (member === undefined) return {};
      return {
        current: score,
        column: 2,
        edit: (value) => ({
          op: "zadd",
          db,
          key,
          field: member,
          value,
          old: old(score),
        }),
        remove: () => ({
          op: "zrem",
          db,
          key,
          field: member,
          old: old(score),
        }),
      };
    }
    case "list": {
      const index = editable(a);
      const current = editable(b);
      if (index === undefined || current === undefined) return {};
      return {
        current,
        column: 1,
        edit: (value) => ({
          op: "lset",
          db,
          key,
          field: index,
          value,
          old: old(current),
        }),
        remove: () => ({ op: "lrem", db, key, field: current, old: old() }),
      };
    }
  }
  return {};
}

/**
 * The staged change on a value row, if any: one with the same op and
 * field as the row's edit or removal.
 */
export function stagedFor(
  staged: readonly DataKeyChange[],
  row: ReturnType<typeof rowChanges>,
): number {
  const own = [row.edit?.(""), row.remove?.()];
  return staged.findIndex((s) =>
    own.some(
      (c) => c && c.op === s.op && c.key === s.key && c.field === s.field,
    ),
  );
}

/** An inline value editor: Enter stages, Esc gives up. */
export function EditCell({
  value,
  onStage,
  onCancel,
}: {
  value: string;
  onStage: (value: string) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState(value);
  const ref = useRef<HTMLInputElement>(null);
  // Opened by a click on the value, so it takes the focus.
  useEffect(() => ref.current?.focus(), []);
  return (
    <input
      ref={ref}
      className="g-field w-full font-mono"
      aria-label="New value"
      value={text}
      onChange={(e) => setText(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") onStage(text);
        if (e.key === "Escape") onCancel();
      }}
      onBlur={onCancel}
    />
  );
}

/** Adding to a collection: a field or member, a value or score. */
export function AddRow({
  type,
  db,
  name,
  onStage,
}: {
  type: string;
  db: number;
  name: string;
  onStage: (change: DataKeyChange) => void;
}) {
  const [field, setField] = useState("");
  const [value, setValue] = useState("");
  const none = { type, value: undefined };
  const needsField = type === "hash" || type === "set" || type === "zset";
  const needsValue = type !== "set";
  if (!["hash", "set", "zset", "list"].includes(type)) return null;
  const ready = (!needsField || field !== "") && (!needsValue || value !== "");
  const stage = (front = false) => {
    const change: DataKeyChange =
      type === "hash"
        ? { op: "hset", db, key: name, field, value, old: none }
        : type === "set"
          ? { op: "sadd", db, key: name, field, old: none }
          : type === "zset"
            ? { op: "zadd", db, key: name, field, value, old: none }
            : { op: front ? "lpush" : "rpush", db, key: name, value };
    onStage(change);
    setField("");
    setValue("");
  };
  return (
    <div className="flex items-center gap-1.5 py-1.5">
      {needsField && (
        <input
          className="g-field min-w-0 flex-1 font-mono"
          aria-label={type === "hash" ? "Field" : "Member"}
          placeholder={type === "hash" ? "field" : "member"}
          value={field}
          onChange={(e) => setField(e.target.value)}
        />
      )}
      {needsValue && (
        <input
          className="g-field min-w-0 flex-1 font-mono"
          aria-label={type === "zset" ? "Score" : "Value"}
          placeholder={type === "zset" ? "score" : "value"}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      )}
      {type === "list" && (
        <Btn small disabled={!ready} onClick={() => stage(true)}>
          Push front
        </Btn>
      )}
      <Btn small disabled={!ready} onClick={() => stage()}>
        {type === "list" ? "Push" : "Add"}
      </Btn>
    </div>
  );
}

/** The key's own changes: its TTL and its deletion. */
export function KeyActions({
  type,
  db,
  name,
  ttlMs,
  onStage,
}: {
  type: string;
  db: number;
  name: string;
  ttlMs: number;
  onStage: (change: DataKeyChange) => void;
}) {
  const [seconds, setSeconds] = useState("");
  const old = { type };
  const n = Number(seconds);
  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1.5">
      <input
        className="g-field w-[110px] tabular-nums"
        aria-label="TTL in seconds"
        placeholder="TTL seconds"
        inputMode="numeric"
        value={seconds}
        onChange={(e) => setSeconds(e.target.value)}
      />
      <Btn
        small
        disabled={!(Number.isInteger(n) && n > 0)}
        onClick={() => {
          onStage({ op: "expire", db, key: name, ttl_ms: n * 1000, old });
          setSeconds("");
        }}
      >
        Set TTL
      </Btn>
      {ttlMs >= 0 && (
        <Btn
          small
          onClick={() =>
            onStage({ op: "expire", db, key: name, ttl_ms: -1, old })
          }
        >
          Remove TTL
        </Btn>
      )}
      <Btn
        small
        danger
        className="ml-auto"
        onClick={() => onStage({ op: "delete", db, key: name, old })}
      >
        Delete key
      </Btn>
    </div>
  );
}
