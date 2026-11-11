import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useShortcuts } from "../../shell/keys";
import {
  useCli,
  useDataSource,
  useKeyPages,
  useKeyValue,
  useSetSensitiveKeys,
} from "./api";
import { byteSize, cellText } from "./cells";
import type { DataPanelView } from "./DataPanel";
import { AddRow, EditCell, KeyActions, rowChanges, stagedFor } from "./KeyEdit";
import { Act, Btn, ErrorText, Kbd, Scroll } from "./kit";
import { type EditActions, ReviewPanel } from "./ReviewPanel";
import type { KeyType } from "./search";
import type {
  DataCell,
  DataKeyChange,
  DataKeyInfo,
  DataPermission,
  DataSchema,
} from "./types";

// The Keys tab of a key-value database (Redis, ADR 0072, mock B): one
// dense grid of keys found with SCAN, filtered by match, type and
// database, and the open key's value in a drawer on the right. Enter
// opens the key full width; clicking a segment of a key's name filters by
// that prefix. Nothing refreshes by itself. In edit mode the drawer
// stages key changes (KeyEdit.tsx), reviewed beside it as one changeset;
// they stay staged across keys of one database.

const keyTypes: KeyType[] = ["string", "hash", "list", "set", "zset", "stream"];

/** A TTL in milliseconds as "28s", "4m", "23h", "3d"; -1 is none. */
function ttlText(ms: number) {
  if (ms < 0) return "none";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86400)}d`;
}

/** c with the regular expression meaning of its characters quoted. */
const quote = (c: string) => c.replace(/[.*+?^${}()|[\]\\/-]/g, "\\$&");

/**
 * Whether key matches a Redis glob (* ? [a-z] [^a] \x), with the server's
 * rules (data.KeyMatch), so the drawer knows a sensitive key needs a
 * reveal before asking for it.
 */
function keyMatch(pattern: string, key: string): boolean {
  let re = "";
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern.charAt(i);
    if (c === "\\" && i + 1 < pattern.length) {
      i++;
      re += quote(pattern.charAt(i));
    } else if (c === "*") {
      re += "[\\s\\S]*";
    } else if (c === "?") {
      re += "[\\s\\S]";
    } else if (c === "[" && pattern.indexOf("]", i + 2) > 0) {
      const end = pattern.indexOf("]", i + 2);
      let body = pattern.slice(i + 1, end);
      const negate = body.startsWith("^");
      if (negate) body = body.slice(1);
      re += `[${negate ? "^" : ""}${body.replace(/[\\\]]/g, "\\$&")}]`;
      i = end;
    } else {
      re += quote(c);
    }
  }
  return new RegExp(`^${re}$`).test(key);
}

/** The prefix filter for a click on segment i of a key's name. */
const prefixMatch = (segments: string[], i: number) =>
  i === segments.length - 1
    ? segments.join(":")
    : `${segments.slice(0, i + 1).join(":")}:*`;

export default function KeysView({
  schema,
  access,
  view,
  onView,
  editing,
  onEditing,
  actions,
}: {
  schema: DataSchema;
  access: DataPermission;
  view: DataPanelView;
  onView: (next: Partial<DataPanelView>) => void;
  editing: boolean;
  onEditing?: (editing: boolean) => void;
  actions?: EditActions;
}) {
  const db = view.db ?? 0;
  const pages = useKeyPages({
    db,
    match: view.match,
    type: view.keyType,
    limit: 100,
  });
  const keys = pages.data?.pages.flatMap((p) => p.keys ?? []) ?? [];
  const total = pages.data?.pages[0]?.total ?? 0;
  const selected = view.key;
  const [full, setFull] = useState(false);
  const [match, setMatch] = useState(view.match ?? "");
  const [patterns, setPatterns] = useState(false);
  const [staged, setStaged] = useState<DataKeyChange[]>([]);
  const client = useQueryClient();
  const source = useDataSource();
  const canEdit = editing && !!actions;
  const matchRef = useRef<HTMLInputElement>(null);
  useEffect(() => setMatch(view.match ?? ""), [view.match]);

  /** Stage a change; a key's new TTL replaces one staged before. */
  const stage = (c: DataKeyChange) =>
    setStaged((all) => [
      ...all.filter(
        (s) => !(c.op === "expire" && s.op === "expire" && s.key === c.key),
      ),
      c,
    ]);
  const open = (k: DataKeyInfo | undefined) => {
    if (!k || k.masked) return;
    onView({ key: k.key, keyHex: k.hex || undefined });
  };
  const at = keys.findIndex((k) => k.key === selected);
  const move = (by: number) =>
    open(keys[Math.max(0, Math.min(keys.length - 1, at + by))]);
  useShortcuts([
    { key: "/", label: "filter keys", run: () => matchRef.current?.focus() },
    { key: "j", label: "next key", run: () => move(1) },
    { key: "k", label: "previous key", run: () => move(-1) },
    ...(selected
      ? [
          { key: "enter", label: "full width", run: () => setFull((f) => !f) },
          {
            key: "escape",
            label: "close",
            run: () =>
              full
                ? setFull(false)
                : onView({ key: undefined, keyHex: undefined }),
          },
        ]
      : []),
  ]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-(--g-line) px-3 py-2">
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault();
            onView({ match: match.trim() || undefined, key: undefined });
          }}
        >
          <input
            ref={matchRef}
            className="g-field w-[300px] font-mono"
            aria-label="Match keys"
            placeholder="match, e.g. user:*"
            value={match}
            onChange={(e) => setMatch(e.target.value)}
          />
        </form>
        <select
          className="g-field w-[130px]"
          aria-label="Key type"
          value={view.keyType ?? ""}
          onChange={(e) =>
            onView({
              keyType: (e.target.value || undefined) as KeyType | undefined,
              key: undefined,
            })
          }
        >
          <option value="">any type</option>
          {keyTypes.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <select
          className="g-field w-[150px]"
          aria-label="Database"
          // A changeset is in one database.
          disabled={staged.length > 0}
          title={
            staged.length > 0
              ? "Commit or discard the staged changes to switch databases"
              : undefined
          }
          value={db}
          onChange={(e) =>
            onView({ db: Number(e.target.value) || undefined, key: undefined })
          }
        >
          {(schema.keyspace ?? [{ db: 0, keys: 0, expires: 0 }]).map((d) => (
            <option key={d.db} value={d.db}>
              db {d.db} · {d.keys.toLocaleString()} keys
            </option>
          ))}
        </select>
        <span className="text-xs text-(--g-mut)">
          {total.toLocaleString()} keys in db {db}
        </span>
        <span className="ml-auto flex items-center gap-2">
          {access.manage && (
            <Btn small ghost onClick={() => setPatterns((v) => !v)}>
              Sensitive keys {schema.sensitive_keys?.length ?? 0}
            </Btn>
          )}
          {onEditing && (
            <Btn
              small
              aria-pressed={editing}
              onClick={() => onEditing(!editing)}
            >
              {editing ? "Editing" : "Edit"} <Kbd>E</Kbd>
            </Btn>
          )}
        </span>
      </div>
      {patterns && (
        <SensitiveKeys
          patterns={schema.sensitive_keys ?? []}
          onDone={() => setPatterns(false)}
        />
      )}
      <div className="flex min-h-0 flex-1">
        {!(full && selected) && (
          <div className="flex min-w-0 flex-1 flex-col">
            <Scroll>
              <table className="g-table">
                <thead>
                  <tr>
                    <th>key</th>
                    <th>type</th>
                    <th className="text-right">length</th>
                    <th className="text-right">size</th>
                    <th>TTL</th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((k, i) => (
                    <tr
                      // Masked keys share a pattern; their order tells them apart.
                      key={k.masked ? `${k.key}\u0000${i}` : k.key}
                      aria-selected={k.key === selected}
                      className={
                        k.key === selected
                          ? "bg-(--g-sel)"
                          : k.masked
                            ? ""
                            : "cursor-pointer"
                      }
                      onClick={() => open(k)}
                    >
                      <td className="max-w-[420px] truncate font-mono text-xs">
                        {k.masked ? (
                          <span className="text-(--g-dim)">
                            {k.key} · masked
                          </span>
                        ) : k.hex ? (
                          <span title="not UTF-8: shown as hex">\x{k.key}</span>
                        ) : (
                          <Segments
                            name={k.key}
                            onPrefix={(m) =>
                              onView({ match: m, key: undefined })
                            }
                          />
                        )}
                      </td>
                      <td className="text-(--g-dim)">{k.type}</td>
                      <td className="text-right tabular-nums">
                        {k.length.toLocaleString()}
                      </td>
                      <td className="text-right tabular-nums">
                        {byteSize(k.bytes)}
                      </td>
                      <td
                        className={
                          k.ttl_ms < 0 ? "text-(--g-dim)" : "tabular-nums"
                        }
                      >
                        {ttlText(k.ttl_ms)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Scroll>
            <div className="flex items-center gap-4 border-t border-(--g-line) px-3 py-2 text-xs text-(--g-mut)">
              <span>
                {keys.length.toLocaleString()}{" "}
                {keys.length === 1 ? "key" : "keys"}
                {pages.hasNextPage ? " so far" : ""} · sizes sampled
              </span>
              <ErrorText error={pages.error} />
              {pages.isFetching && <span>Scanning</span>}
              {pages.hasNextPage && !pages.isFetching && (
                <Btn
                  small
                  className="ml-auto"
                  onClick={() => pages.fetchNextPage()}
                >
                  Scan more
                </Btn>
              )}
            </div>
          </div>
        )}
        {selected && (
          <KeyPane
            key={`${db}:${selected}`}
            db={db}
            name={selected}
            hex={!!view.keyHex}
            sensitive={(schema.sensitive_keys ?? []).some((p) =>
              keyMatch(p, selected),
            )}
            access={access}
            staged={canEdit ? staged : undefined}
            onStage={stage}
            onUnstage={(i) => setStaged((all) => all.filter((_, j) => j !== i))}
            full={full}
            onFull={() => setFull((f) => !f)}
            onClose={() => {
              setFull(false);
              onView({ key: undefined, keyHex: undefined });
            }}
          />
        )}
        {canEdit && actions && staged.length > 0 && (
          <ReviewPanel
            table={selected ?? `db ${db}`}
            body={{ key_changes: staged }}
            confirmCommit={access.confirm_commit && access.level !== "propose"}
            propose={access.level === "propose"}
            actions={actions}
            onRestoreKey={(i) =>
              setStaged((all) => all.filter((_, j) => j !== i))
            }
            onDiscard={() => setStaged([])}
            onCommitted={() => {
              setStaged([]);
              client.invalidateQueries({ queryKey: [...source.key, "keys"] });
            }}
          />
        )}
      </div>
    </div>
  );
}

/** A key's name with each ':' segment a prefix filter. */
function Segments({
  name,
  onPrefix,
}: {
  name: string;
  onPrefix: (match: string) => void;
}) {
  const segments = name.split(":");
  return (
    <>
      {segments.map((s, i) => (
        // A segment's text can repeat in a name; its place is its identity.
        <span key={`${i}:${s}`}>
          {i > 0 && <span className="text-(--g-dim)">:</span>}
          <button
            type="button"
            // min-w-0: inline in the name, so "1" in user:1:name isn't
            // centered in the 24px every other button gets.
            className="min-w-0 hover:text-(--g-link)"
            title={`Show keys matching ${prefixMatch(segments, i)}`}
            onClick={(e) => {
              e.stopPropagation();
              onPrefix(prefixMatch(segments, i));
            }}
          >
            {s}
          </button>
        </span>
      ))}
    </>
  );
}

/** The open key: its facts and its value a page at a time. */
function KeyPane({
  db,
  name,
  hex,
  sensitive,
  access,
  staged,
  onStage,
  onUnstage,
  full,
  onFull,
  onClose,
}: {
  db: number;
  name: string;
  hex: boolean;
  sensitive: boolean;
  access: DataPermission;
  /** Edit mode's staged changes (undefined: not editing). */
  staged?: readonly DataKeyChange[];
  onStage: (change: DataKeyChange) => void;
  onUnstage: (index: number) => void;
  full: boolean;
  onFull: () => void;
  onClose: () => void;
}) {
  const cli = useCli();
  const [reveal, setReveal] = useState(false);
  const canRead = !sensitive || reveal;
  const value = useKeyValue(
    canRead
      ? { db, key: name, hex, reveal: reveal || undefined, limit: 100 }
      : null,
  );
  const first = value.data?.pages[0];
  const rows = value.data?.pages.flatMap((p) => p.rows ?? []) ?? [];
  const cols = first?.columns ?? [];
  // A key named in hex can't be written: changes name keys as text.
  const editable = !!staged && !hex && !!first;
  const [editingRow, setEditingRow] = useState<number>();
  const flags = [db && `--db ${db}`, hex && "--hex", reveal && "--reveal"]
    .filter(Boolean)
    .join(" ");
  return (
    <aside
      aria-label="Key"
      className={`g-aside ${full ? "min-w-0 flex-1 border-l-0" : "w-[360px] shrink-0"}`}
    >
      <div className="flex items-center gap-2">
        <span className="min-w-0 truncate font-mono font-semibold">
          {hex ? `\\x${name}` : name}
        </span>
        <Btn small ghost className="ml-auto" onClick={onFull} title="Enter">
          {full ? "Side" : "Full width"}
        </Btn>
        <Btn small ghost onClick={onClose} aria-label="Close" title="Esc">
          ×
        </Btn>
      </div>
      {first && (
        <dl className="my-2 grid grid-cols-[80px_1fr] gap-x-3 gap-y-1 text-xs">
          <dt className="text-(--g-mut)">type</dt>
          <dd>
            {first.type}
            {first.encoding && (
              <span className="text-(--g-dim)"> · {first.encoding}</span>
            )}
          </dd>
          <dt className="text-(--g-mut)">length</dt>
          <dd className="tabular-nums">{first.length.toLocaleString()}</dd>
          <dt className="text-(--g-mut)">size</dt>
          <dd className="tabular-nums">{byteSize(first.bytes)} (sampled)</dd>
          <dt className="text-(--g-mut)">TTL</dt>
          <dd className="tabular-nums">{ttlText(first.ttl_ms)}</dd>
        </dl>
      )}
      {staged && hex && (
        <p className="text-xs text-(--g-dim)">
          This key's name isn't text, so it can't be changed here. Use Commands.
        </p>
      )}
      {editable && first && (
        <KeyActions
          type={first.type}
          db={db}
          name={name}
          ttlMs={first.ttl_ms}
          onStage={onStage}
        />
      )}
      {!canRead ? (
        <div className="my-2 flex flex-col gap-2 text-xs text-(--g-dim)">
          <span>
            This key matches a sensitive pattern.
            {access.sensitive !== "visible" && " Its value is masked for you."}
          </span>
          {access.sensitive === "visible" && (
            <Act
              small
              cli={cli(`dock db key '${name}' ${flags} --reveal`.trim())}
              onClick={() => setReveal(true)}
            >
              Reveal (audited)
            </Act>
          )}
        </div>
      ) : (
        <>
          <ErrorText error={value.error} />
          {first && cols.length === 0 && (
            <p className="text-xs text-(--g-dim)">
              A module type: its value isn't shown here. Read it in Commands.
            </p>
          )}
          {cols.length > 0 && (
            // The drawer scrolls as a whole: the table never shrinks.
            <div className="shrink-0">
              <Scroll>
                <table className="g-table">
                  <thead>
                    <tr>
                      {cols.map((c) => (
                        <th
                          key={c.name}
                          className={c.name === "#" ? "w-12 text-right" : ""}
                        >
                          {c.name}
                        </th>
                      ))}
                      {editable && <th className="w-16" />}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((r, i) => {
                      const cells = r as DataCell[];
                      const row = editable
                        ? rowChanges(first.type, name, db, cells)
                        : {};
                      const at = staged ? stagedFor(staged, row) : -1;
                      const change = staged?.[at];
                      const removed = change && change.value === undefined;
                      return (
                        // Value rows have no identity but their place.
                        <tr
                          key={`${i}`}
                          className={
                            removed ? "text-(--g-dim) line-through" : ""
                          }
                        >
                          {cells.map((cell, j) => (
                            <td
                              key={cols[j]?.name ?? j}
                              className={`max-w-[520px] truncate font-mono text-xs ${cols[j]?.name === "#" ? "text-right text-(--g-dim)" : ""}`}
                              title={
                                typeof cell === "string" ? cell : undefined
                              }
                            >
                              {editingRow === i &&
                              j === row.column &&
                              row.edit &&
                              row.current !== undefined ? (
                                <EditCell
                                  value={row.current}
                                  onStage={(v) => {
                                    if (v !== row.current && row.edit)
                                      onStage(row.edit(v));
                                    setEditingRow(undefined);
                                  }}
                                  onCancel={() => setEditingRow(undefined)}
                                />
                              ) : change && !removed && j === row.column ? (
                                <span className="text-(--g-warn)">
                                  {change.value}
                                </span>
                              ) : (
                                cellText(cell, cols[j]?.type_name ?? "")
                              )}
                            </td>
                          ))}
                          {editable && (
                            <td className="text-right text-xs whitespace-nowrap">
                              {change ? (
                                <button
                                  type="button"
                                  className="text-(--g-mut) hover:text-(--g-text)"
                                  onClick={() => onUnstage(at)}
                                >
                                  Undo
                                </button>
                              ) : (
                                <>
                                  {row.edit && row.current !== undefined && (
                                    <button
                                      type="button"
                                      className="text-(--g-mut) hover:text-(--g-text)"
                                      onClick={() => setEditingRow(i)}
                                    >
                                      Edit
                                    </button>
                                  )}
                                  {row.remove && (
                                    <button
                                      type="button"
                                      aria-label="Remove"
                                      title="Remove"
                                      className="ml-2 text-(--g-mut) hover:text-(--g-fail)"
                                      onClick={() =>
                                        row.remove && onStage(row.remove())
                                      }
                                    >
                                      ×
                                    </button>
                                  )}
                                </>
                              )}
                            </td>
                          )}
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </Scroll>
            </div>
          )}
          {editable && first && (
            <AddRow type={first.type} db={db} name={name} onStage={onStage} />
          )}
          <div className="flex items-center gap-3 py-2 text-xs text-(--g-mut)">
            {first && (
              <span>
                {rows.length.toLocaleString()} of{" "}
                {first.type === "string" ? "1" : first.length.toLocaleString()}
              </span>
            )}
            {value.hasNextPage && (
              <Btn
                small
                className="ml-auto"
                disabled={value.isFetching}
                onClick={() => value.fetchNextPage()}
              >
                Next 100
              </Btn>
            )}
          </div>
        </>
      )}
    </aside>
  );
}

/** An admin's list of sensitive key patterns, one per line. */
function SensitiveKeys({
  patterns,
  onDone,
}: {
  patterns: readonly string[];
  onDone: () => void;
}) {
  const cli = useCli();
  const save = useSetSensitiveKeys();
  const [text, setText] = useState(patterns.join("\n"));
  const list = text
    .split("\n")
    .map((p) => p.trim())
    .filter(Boolean);
  return (
    <div className="flex flex-wrap items-start gap-3 border-b border-(--g-line) px-3 py-2.5 text-xs">
      <textarea
        className="g-field h-20 w-[300px] font-mono"
        aria-label="Sensitive key patterns, one per line"
        placeholder={"session:*\nauth:*"}
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <span className="max-w-[420px] text-(--g-dim)">
        Keys matching these patterns show by pattern to callers who see
        sensitive values masked, who also lose the console. Others read them
        with an audited reveal.
      </span>
      <span className="ml-auto flex items-center gap-2">
        <ErrorText error={save.error} />
        <Btn small onClick={onDone}>
          Cancel
        </Btn>
        <Act
          small
          primary
          cli={cli(
            `dock db sensitive set --keys ${list.map((p) => `'${p}'`).join(" ")}`.trim(),
          )}
          disabled={save.isPending}
          onClick={() => save.mutate(list, { onSuccess: onDone })}
        >
          Save
        </Act>
      </span>
    </div>
  );
}
