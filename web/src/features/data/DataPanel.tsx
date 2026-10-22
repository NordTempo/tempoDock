import { lazy, type ReactNode, Suspense, useState } from "react";
import { useShortcuts } from "../../shell/keys";
import {
  useChangesets,
  useDataInfo,
  useDataSchema,
  useDataSource,
  useEditActions,
} from "./api";
import { ChangesView } from "./ChangesView";
import { tableName } from "./cells";
import { SchemaView } from "./SchemaView";
import { StatusView, Why } from "./StatusView";
import { TableList } from "./TableList";
import { TablesView } from "./TablesView";
import type { DataFilter, DataLevel, DataPermission } from "./types";

// CodeMirror loads only when the SQL tab opens.
const SqlView = lazy(() => import("./SqlView"));

/** What the panel shows; the Data route keeps it in the URL's search. */
export type DataPanelView = {
  tab: "tables" | "sql" | "schema" | "changes";
  /** "schema.name"; the first table when unset. */
  table?: string;
  /** A column, "-column" for descending. */
  order?: string;
  filters?: DataFilter[];
};

const ranks: Record<DataLevel, number> = {
  none: 0,
  schema: 1,
  read: 2,
  propose: 3,
  write: 4,
};
const atLeast = (level: DataLevel, wanted: DataLevel) =>
  ranks[level] >= ranks[wanted];

/** What the caller's level allows, in the header's words. */
const levelText: Record<DataLevel, string> = {
  none: "no access",
  schema: "schema only",
  read: "read",
  propose: "read, propose changes",
  write: "read, write",
};

/**
 * The database panel inside a DataSourceContext (a Dock service's Data
 * tab, a tempoBase connection): header line (engine, mode, role, the
 * caller's access and why), Tables, SQL, Schema and Changes. Controlled:
 * `view` and `onView` carry everything that belongs in the URL. Edit mode
 * is per mount, off by default, never remembered.
 */
export function DataPanel({
  view,
  onView,
  height = "h-[calc(100vh-240px)] min-h-[480px]",
}: {
  view: DataPanelView;
  onView: (next: DataPanelView) => void;
  /** Tailwind height classes for the panel. */
  height?: string;
}) {
  const { roles, confirmName } = useDataSource();
  const info = useDataInfo();
  const access = info.data?.access;
  const ready =
    info.data?.status === "ready" && !!access && access.level !== "none";
  const schema = useDataSchema(ready);
  const [editing, setEditing] = useState(false);
  const [why, setWhy] = useState(false);
  const edit = useEditActions(access, () => {
    setEditing(false);
    onView({ ...view, tab: "changes" });
  });
  const canEdit = !!edit;
  const showChanges =
    ready && (access.approve || atLeast(access.level, "propose"));
  const changesets = useChangesets(showChanges);
  const pending = (changesets.data ?? []).filter(
    (c) => c.status === "pending",
  ).length;

  const tabs = [
    { tab: "tables", label: "Tables", key: "t" },
    { tab: "sql", label: "SQL", key: "q" },
    { tab: "schema", label: "Schema", key: "s" },
    ...(showChanges
      ? [{ tab: "changes", label: "Changes", key: "c" } as const]
      : []),
  ] as const;

  useShortcuts([
    ...tabs.map((t) => ({
      key: t.key,
      label: t.label.toLowerCase(),
      run: () => onView({ ...view, tab: t.tab }),
    })),
    ...(canEdit && view.tab === "tables"
      ? [
          {
            key: "e",
            label: editing ? "stop editing" : "edit",
            run: () => setEditing((e) => !e),
          },
        ]
      : []),
  ]);

  if (info.error) return <p className="text-fail">{info.error.message}</p>;
  if (!info.data || !access)
    return <p className="text-mut">Connecting to the database</p>;
  if (!ready) return <StatusView info={info.data} />;

  const tables = schema.data?.tables ?? [];
  const selected = tables.find((t) => tableName(t) === view.table) ?? tables[0];
  const selectedName = selected && tableName(selected);
  const production = access.confirm_commit;
  // Edit mode only applies to the Tables tab; it survives a look elsewhere.
  const writing = editing && view.tab === "tables";
  // Masked callers read through the masked role, which can't select
  // sensitive columns: SQL on them comes back as an error frame.
  const reader = access.sensitive === "masked" ? roles.masked : roles.reader;
  const openTable = (table: string) =>
    onView({ ...view, table, filters: [], order: undefined });

  return (
    <div className={`flex flex-col gap-3 ${height}`}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <nav aria-label="Data" className="seg">
          {tabs.map((t) => (
            <button
              key={t.tab}
              type="button"
              aria-pressed={view.tab === t.tab}
              onClick={() => onView({ ...view, tab: t.tab })}
            >
              {t.label}
              {t.tab === "changes" && pending > 0 && (
                <span className="text-warn"> {pending}</span>
              )}{" "}
              <kbd>{t.key.toUpperCase()}</kbd>
            </button>
          ))}
        </nav>
        <span className="font-mono text-xs text-mut">
          {info.data.engine} {info.data.server_version} ·{" "}
          {writing ? (
            <span className="text-warn">
              editing{production && ` ${confirmName}`}
            </span>
          ) : (
            <span className="text-white">read-only</span>
          )}{" "}
          as {writing ? roles.writer : reader} · you:{" "}
          <span className="text-white">{levelText[access.level]}</span>
          {access.sensitive === "masked" && ", sensitive masked"}
          {access.approve && ", approve"}{" "}
          <button
            type="button"
            className="lnk"
            aria-expanded={why}
            onClick={() => setWhy((w) => !w)}
          >
            why
          </button>
          {info.data.used_by?.length
            ? ` · used by ${info.data.used_by.join(", ")}`
            : ""}
        </span>
        {canEdit && view.tab === "tables" && (
          <button
            type="button"
            className={`btn sm ml-auto ${editing ? "text-warn" : ""}`}
            aria-pressed={editing}
            onClick={() => setEditing((e) => !e)}
          >
            {editing ? "Stop editing" : "Edit"} <kbd>E</kbd>
          </button>
        )}
      </div>
      {why && <Why access={access} />}

      {schema.error ? (
        <p className="text-fail">{schema.error.message}</p>
      ) : !schema.data ? (
        <p className="text-mut">Reading the catalog</p>
      ) : view.tab === "changes" && showChanges ? (
        <ChangesView access={access} />
      ) : view.tab === "sql" ? (
        !atLeast(access.level, "read") ? (
          <Limited access={access}>
            Your access is schema: tables and columns, no rows or SQL.
          </Limited>
        ) : (
          <Suspense fallback={<p className="text-mut">Loading the editor</p>}>
            <SqlView tables={tables} />
          </Suspense>
        )
      ) : (
        <div className="flex min-h-0 flex-1 gap-5">
          <div className="flex w-[220px] shrink-0 flex-col">
            <TableList
              tables={tables}
              selected={selectedName}
              onSelect={openTable}
            />
          </div>
          {!selected ? (
            <p className="text-mut">This database has no tables yet.</p>
          ) : view.tab === "schema" ? (
            <SchemaView
              table={selected}
              tables={tables}
              manage={access.manage}
              onOpen={openTable}
            />
          ) : !atLeast(access.level, "read") ? (
            <Limited access={access}>
              Your access is schema: tables and columns, no rows. See the Schema
              tab.
            </Limited>
          ) : (
            <TablesView
              key={selectedName}
              info={info.data}
              table={selected}
              query={{
                table: selectedName ?? "",
                order: view.order,
                filters: view.filters,
              }}
              editing={editing}
              actions={edit}
              onQuery={(q) => onView({ ...view, ...q })}
            />
          )}
        </div>
      )}
    </div>
  );
}

function Limited({
  access,
  children,
}: {
  access: DataPermission;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2 py-2">
      <p>{children}</p>
      <Why access={access} />
    </div>
  );
}
