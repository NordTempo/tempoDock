import type { DataChange } from "./types";

// Staged edits of one table (ADR 0016 section 5): buffered in the browser,
// highlighted in the grid, sent as one changeset on commit. Nothing here
// talks to the server. Use with useReducer(editsReducer, noEdits).

/** One row's staged change, keyed by its primary key. */
export type RowEdit = {
  key: Record<string, string>;
  /** Values the editor saw, per edited column (the conflict check). */
  old: Record<string, string | null>;
  /** Values to set, per edited column. */
  new: Record<string, string | null>;
  deleted: boolean;
};

/** Staged edits by row id (see rowId). */
export type Edits = Readonly<Record<string, RowEdit>>;

export const noEdits: Edits = {};

/** A stable id for a row from its primary key values. */
export const rowId = (key: Record<string, string>) =>
  JSON.stringify(Object.values(key));

export type EditAction =
  | {
      type: "set";
      key: Record<string, string>;
      column: string;
      old: string | null;
      value: string | null;
    }
  | { type: "delete"; key: Record<string, string> }
  | { type: "restore"; key: Record<string, string> }
  | { type: "discard" };

export function editsReducer(edits: Edits, action: EditAction): Edits {
  if (action.type === "discard") return noEdits;
  const id = rowId(action.key);
  const current: RowEdit = edits[id] ?? {
    key: action.key,
    old: {},
    new: {},
    deleted: false,
  };
  let next: RowEdit;
  switch (action.type) {
    case "set": {
      const old =
        action.column in current.old ? current.old[action.column] : action.old;
      const values = { ...current.new, [action.column]: action.value };
      const olds = { ...current.old, [action.column]: old ?? null };
      // Setting a cell back to what it was unstages it.
      if (action.value === old) {
        delete values[action.column];
        delete olds[action.column];
      }
      next = { ...current, old: olds, new: values };
      break;
    }
    case "delete":
      next = { ...current, deleted: true };
      break;
    case "restore":
      next = { ...current, deleted: false, old: {}, new: {} };
      break;
  }
  const { [id]: _, ...rest } = edits;
  return next.deleted || Object.keys(next.new).length
    ? { ...rest, [id]: next }
    : rest;
}

/** The changeset for a table's staged edits, deletes after updates. */
export function toChanges(table: string, edits: Edits): DataChange[] {
  const rows = Object.values(edits);
  return [
    ...rows
      .filter((r) => !r.deleted)
      .map(
        (r): DataChange => ({
          op: "update",
          table,
          key: r.key,
          old: r.old,
          new: r.new,
        }),
      ),
    ...rows
      .filter((r) => r.deleted)
      .map((r): DataChange => ({ op: "delete", table, key: r.key })),
  ];
}
