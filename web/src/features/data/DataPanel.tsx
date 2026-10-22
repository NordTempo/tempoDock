import { lazy, type ReactNode, Suspense, useState } from "react";
import type { ServiceRef } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useDataInfo, useDataSchema } from "./api";
import { tableName } from "./cells";
import type { EditActions } from "./ReviewPanel";
import { SchemaView } from "./SchemaView";
import { StatusView, Why } from "./StatusView";
import { TableList } from "./TableList";
import { TablesView } from "./TablesView";
import type { DataFilter, DataLevel, DataPermission } from "./types";

// CodeMirror loads only when the SQL tab opens.
const SqlView = lazy(() => import("./SqlView"));

/** What the panel shows; phase 3 keeps it in the URL's search params. */
export type DataPanelView = {
  tab: "tables" | "sql" | "schema";
  /** "schema.name"; the first table when unset. */
  table?: string;
  /** A column, "-column" for descending. */
  order?: string;
  filters?: DataFilter[];
};

const tabs = [
  { tab: "tables", label: "Tables", key: "t" },
  { tab: "sql", label: "SQL", key: "q" },
  { tab: "schema", label: "Schema", key: "s" },
] as const;

const ranks: Record<DataLevel, number> = {
  none: 0,
  schema: 1,
  read: 2,
  propose: 3,
  write: 4,
};
const atLeast = (level: DataLevel, wanted: DataLevel) =>
  ranks[level] >= ranks[wanted];

/**
 * The Data tab of a database service: header line (engine, mode, role,
 * access), Tables, SQL and Schema. Controlled: `view` and `onView` carry
 * everything that belongs in the URL. Edit mode is per mount, off by
 * default, never remembered; `edit` wires commits (phase 4).
 */
export function DataPanel({
  serviceRef,
  view,
  onView,
  edit,
  height = "h-[calc(100vh-240px)] min-h-[480px]",
}: {
  serviceRef: ServiceRef;
  view: DataPanelView;
  onView: (next: DataPanelView) => void;
  edit?: EditActions;
  /** Tailwind height classes for the panel. */
  height?: string;
}) {
  const info = useDataInfo(serviceRef);
  const access = info.data?.access;
  const ready =
    info.data?.status === "ready" && !!access && access.level !== "none";
  const schema = useDataSchema(serviceRef, ready);
  const [editing, setEditing] = useState(false);
  const flags = `-s ${serviceRef.service} -e ${serviceRef.environment}`;
  const canEdit = !!edit && !!access && atLeast(access.level, "propose");

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
  if (!ready)
    return (
      <StatusView serviceRef={serviceRef} info={info.data} flags={flags} />
    );

  const tables = schema.data?.tables ?? [];
  const selected = tables.find((t) => tableName(t) === view.table) ?? tables[0];
  const selectedName = selected && tableName(selected);
  const production = access.confirm_commit;
  const masked =
    access.sensitive === "masked" &&
    tables.some((t) => (t.columns ?? []).some((c) => c.sensitive));
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
              {t.label} <kbd>{t.key.toUpperCase()}</kbd>
            </button>
          ))}
        </nav>
        <span
          className="font-mono text-xs text-mut"
          title={(access.why ?? []).map((s) => s.detail).join("\n")}
        >
          {info.data.engine} {info.data.server_version} ·{" "}
          {editing ? (
            <span className="text-warn">
              editing{production && ` ${serviceRef.environment}`}
            </span>
          ) : (
            <span className="text-white">read-only</span>
          )}{" "}
          as {editing ? "dock_writer" : "dock_reader"} · access {access.level}
          {access.sensitive === "masked" && ", sensitive masked"}
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

      {schema.error ? (
        <p className="text-fail">{schema.error.message}</p>
      ) : !schema.data ? (
        <p className="text-mut">Reading the catalog</p>
      ) : view.tab === "sql" ? (
        !atLeast(access.level, "read") ? (
          <Limited access={access}>
            Your access is schema: tables and columns, no rows or SQL.
          </Limited>
        ) : masked ? (
          <Limited access={access}>
            This database has sensitive columns you see masked, so free SQL is
            off until masking is enforced in Postgres. Browse tables instead.
          </Limited>
        ) : (
          <Suspense fallback={<p className="text-mut">Loading the editor</p>}>
            <SqlView serviceRef={serviceRef} tables={tables} flags={flags} />
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
              flags={flags}
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
              serviceRef={serviceRef}
              info={info.data}
              table={selected}
              query={{
                table: selectedName ?? "",
                order: view.order,
                filters: view.filters,
              }}
              editing={editing}
              actions={edit}
              flags={flags}
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
