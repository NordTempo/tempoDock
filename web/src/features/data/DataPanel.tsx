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
import { Kbd } from "./kit";
import { levelText } from "./policy";
import { SchemaView } from "./SchemaView";
import { StatusView, Why } from "./StatusView";
import { TableList } from "./TableList";
import { TablesView } from "./TablesView";
import type { DataFilter, DataLevel, DataPermission } from "./types";

// CodeMirror loads only when the SQL tab opens.
const SqlView = lazy(() => import("./SqlView"));

/** What the panel shows; the host route keeps it in the URL's search. */
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
export const atLeast = (level: DataLevel, wanted: DataLevel) =>
  ranks[level] >= ranks[wanted];

/**
 * The database panel (style G) inside a DataSourceContext: a Dock
 * service's Data tab or a tempoBase connection. A bar with the tabs
 * (Tables, SQL, Schema, Changes) and the caller's access and why, then the
 * tab. Controlled: `view` and `onView` carry everything that belongs in the
 * URL. Edit mode is per mount, off by default, never remembered.
 * `tableList` false leaves the table list to the host (tempoBase shows it
 * in its sidebar).
 */
export function DataPanel({
  view,
  onView,
  className = "h-[calc(100vh-240px)] min-h-[480px]",
  tableList = true,
}: {
  view: DataPanelView;
  onView: (next: DataPanelView) => void;
  /** Sizing classes for the panel. */
  className?: string;
  tableList?: boolean;
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

  const shell = (children: ReactNode) => (
    <div className={`g flex flex-col ${className}`}>{children}</div>
  );

  if (info.error)
    return shell(<p className="p-5 text-(--g-fail)">{info.error.message}</p>);
  if (!info.data || !access)
    return shell(
      <p className="p-5 text-(--g-mut)">Connecting to the database</p>,
    );
  if (!ready) return shell(<StatusView info={info.data} />);

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

  return shell(
    <>
      <div className="flex min-h-11 flex-wrap items-center gap-x-4 gap-y-1 border-b border-(--g-line) px-3 py-1.5">
        <nav aria-label="Data" className="g-tabs">
          {tabs.map((t) => (
            <button
              key={t.tab}
              type="button"
              aria-pressed={view.tab === t.tab}
              onClick={() => onView({ ...view, tab: t.tab })}
            >
              {t.label}
              {t.tab === "changes" && pending > 0 && (
                <span className="text-(--g-warn)">{pending}</span>
              )}
              <Kbd>{t.key.toUpperCase()}</Kbd>
            </button>
          ))}
        </nav>
        <span className="ml-auto text-xs text-(--g-mut)">
          {info.data.engine} {info.data.server_version} ·{" "}
          {writing ? (
            <span className="text-(--g-warn)">
              editing{production && ` ${confirmName}`}
            </span>
          ) : (
            "read only"
          )}{" "}
          as{" "}
          <span className="font-mono">{writing ? roles.writer : reader}</span> ·
          you:{" "}
          <span className="text-(--g-text)">{levelText[access.level]}</span>
          {access.sensitive === "masked" && ", sensitive masked"}
          {access.approve && ", approve"}{" "}
          <button
            type="button"
            className="g-lnk"
            aria-expanded={why}
            onClick={() => setWhy((w) => !w)}
          >
            why
          </button>
          {info.data.used_by?.length
            ? ` · used by ${info.data.used_by.join(", ")}`
            : ""}
        </span>
      </div>
      {why && (
        <div className="border-b border-(--g-line) px-5 py-2">
          <Why access={access} />
        </div>
      )}

      {schema.error ? (
        <p className="p-5 text-(--g-fail)">{schema.error.message}</p>
      ) : !schema.data ? (
        <p className="p-5 text-(--g-mut)">Reading the catalog</p>
      ) : view.tab === "changes" && showChanges ? (
        <ChangesView access={access} />
      ) : view.tab === "sql" ? (
        !atLeast(access.level, "read") ? (
          <Limited access={access}>
            Your access is schema: tables and columns, no rows or SQL.
          </Limited>
        ) : (
          <Suspense
            fallback={<p className="p-5 text-(--g-mut)">Loading the editor</p>}
          >
            <SqlView tables={tables} />
          </Suspense>
        )
      ) : (
        <div className="flex min-h-0 flex-1">
          {tableList && (
            <div className="flex w-[240px] shrink-0 flex-col border-r border-(--g-line) bg-(--g-side) p-2">
              <TableList
                tables={tables}
                selected={selectedName}
                onSelect={openTable}
              />
            </div>
          )}
          {!selected ? (
            <p className="p-5 text-(--g-mut)">
              This database has no tables yet.
            </p>
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
              onEditing={canEdit ? setEditing : undefined}
              actions={edit}
              onQuery={(q) => onView({ ...view, ...q })}
            />
          )}
        </div>
      )}
    </>,
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
    <div className="flex flex-col gap-2 p-5">
      <p>{children}</p>
      <Why access={access} />
    </div>
  );
}
