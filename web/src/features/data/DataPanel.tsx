import {
  lazy,
  type ReactNode,
  Suspense,
  useEffect,
  useRef,
  useState,
} from "react";
import { useShortcuts } from "../../shell/keys";
import { ago } from "../../ui/format";
import {
  useChangesets,
  useCli,
  useDataInfo,
  useDataSchema,
  useDataSetup,
  useDataSource,
  useEditActions,
} from "./api";
import { ChangesView } from "./ChangesView";
import { tableName } from "./cells";
import { engines } from "./engines";
import { Act, ErrorText } from "./kit";
import { SchemaView } from "./SchemaView";
import { StatusView, Why } from "./StatusView";
import type { KeyType } from "./search";
import { TableList } from "./TableList";
import { TablesView } from "./TablesView";
import type {
  DataFilter,
  DataInfo,
  DataInsightView,
  DataLevel,
  DataPermission,
} from "./types";

// CodeMirror loads only when the SQL tab opens; Health when it does.
const SqlView = lazy(() => import("./SqlView"));
const HealthView = lazy(() => import("./HealthView"));
// The key browser (Redis) loads only for key-value databases.
const KeysView = lazy(() => import("./KeysView"));

/** What the panel shows; the host route keeps it in the URL's search. */
export type DataPanelView = {
  tab: "tables" | "keys" | "sql" | "schema" | "changes" | "health";
  /** "schema.name"; the first table when unset. */
  table?: string;
  /** A column, "-column" for descending. */
  order?: string;
  filters?: DataFilter[];
  /** The Health tab's view; the overview when unset. */
  insight?: DataInsightView;
  /** Redis: the logical database (0 when unset), the key filter, the open key. */
  db?: number;
  match?: string;
  keyType?: KeyType;
  key?: string;
  keyHex?: boolean;
};

const tabLabels: Record<DataPanelView["tab"], string> = {
  tables: "Tables",
  keys: "Keys",
  sql: "SQL",
  schema: "Schema",
  changes: "Changes",
  health: "Health",
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

/** What the caller may do, after "you can" in the panel's bar. */
// What the caller may do, in plain words for the panel's header (the
// database roles behind it are in Why).
const canText: Record<DataLevel, string> = {
  none: "You can't open this database",
  schema: "You can see tables and columns, not rows",
  read: "You can read rows",
  propose: "You can read rows and propose changes",
  write: "You can read and change rows",
};

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
  const { confirmName } = useDataSource();
  const info = useDataInfo();
  const access = info.data?.access;
  const ready =
    info.data?.status === "ready" && !!access && access.level !== "none";
  const schema = useDataSchema(ready);
  const [editing, setEditing] = useState(false);
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

  const engine = engines[info.data?.engine ?? "postgres"];
  const tabList = engine.tabs
    .filter((tab) => tab !== "changes" || showChanges)
    .map((tab) => ({ tab, label: engine.labels?.[tab] ?? tabLabels[tab] }));
  // A tab the engine doesn't have (a link from another database) opens
  // its first one.
  const tab = engine.tabs.includes(view.tab) ? view.tab : engine.tabs[0];
  // Digits pick the nth tab.
  const tabs = tabList.map((t, i) => ({ ...t, key: String(i + 1) }));
  // SQL the Health tab hands over ("Explain in SQL"); the editor opens
  // with it once.
  const [sqlDraft, setSqlDraft] = useState<{ sql: string; n: number }>();

  // Until the database is set up and readable the panel shows only its
  // status, so no tab or edit keys are bound (or listed in the legend).
  useShortcuts(
    ready
      ? [
          ...tabs.map((t) => ({
            key: t.key,
            label: t.label.toLowerCase(),
            run: () => onView({ ...view, tab: t.tab }),
          })),
          ...(canEdit && tab === "tables"
            ? [
                {
                  key: "e",
                  label: editing ? "stop editing" : "edit",
                  run: () => setEditing((e) => !e),
                },
              ]
            : []),
        ]
      : [],
  );

  const shell = (children: ReactNode) => (
    <div className={`g flex flex-col ${className}`}>{children}</div>
  );
  // Until it's ready the panel is a few lines, not a full-height box.
  const status = (children: ReactNode) => (
    <div className="g flex flex-col">{children}</div>
  );

  if (info.error)
    return status(<p className="p-5 text-(--g-fail)">{info.error.message}</p>);
  if (!info.data || !access)
    return status(
      <p className="p-5 text-(--g-mut)">Connecting to the database</p>,
    );
  if (!ready) return status(<StatusView info={info.data} />);

  const tables = schema.data?.tables ?? [];
  const selected = tables.find((t) => tableName(t) === view.table) ?? tables[0];
  const selectedName = selected && tableName(selected);
  const production = access.confirm_commit;
  // Edit mode only applies to the Tables tab; it survives a look elsewhere.
  const writing = editing && tab === "tables";
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
              aria-pressed={tab === t.tab}
              // The digit is the key; shown on hover and in the ? help, not
              // on the tab, where it read as a count.
              title={`${t.label} (${t.key})`}
              onClick={() => onView({ ...view, tab: t.tab })}
            >
              {t.label}
              {t.tab === "changes" && pending > 0 && (
                <span className="text-(--g-warn)">{pending}</span>
              )}
            </button>
          ))}
        </nav>
        <span className="ml-auto text-xs text-(--g-mut)">
          {info.data.engine} {info.data.server_version} ·{" "}
          {writing && (
            <span className="text-(--g-warn)">
              Editing{production && ` ${confirmName}`} ·{" "}
            </span>
          )}
          <span className="text-(--g-text)">
            {canText[access.level]}
            {access.approve && ", and approve changes others propose"}
          </span>
          {access.sensitive === "masked" && "; sensitive columns are masked"}{" "}
          <WhyLink info={info.data} />
          {info.data.used_by?.length
            ? ` · used by ${info.data.used_by.join(", ")}`
            : ""}
        </span>
      </div>

      {schema.error ? (
        <p className="p-5 text-(--g-fail)">{schema.error.message}</p>
      ) : !schema.data ? (
        <p className="p-5 text-(--g-mut)">Reading the catalog</p>
      ) : tab === "keys" ? (
        !atLeast(access.level, "read") ? (
          <Limited access={access}>
            Your access is schema: the databases and their key counts, no keys.
            Key names are data.
          </Limited>
        ) : (
          <Suspense
            fallback={<p className="p-5 text-(--g-mut)">Loading the keys</p>}
          >
            <KeysView
              schema={schema.data}
              access={access}
              view={view}
              onView={(next) => onView({ ...view, ...next })}
            />
          </Suspense>
        )
      ) : tab === "health" ? (
        <Suspense
          fallback={<p className="p-5 text-(--g-mut)">Loading Health</p>}
        >
          <HealthView
            access={access}
            view={view.insight ?? "overview"}
            onView={(insight) =>
              onView({
                ...view,
                insight: insight === "overview" ? undefined : insight,
              })
            }
            onExplain={(sql) => {
              setSqlDraft((d) => ({ sql, n: (d?.n ?? 0) + 1 }));
              onView({ ...view, tab: "sql" });
            }}
          />
        </Suspense>
      ) : tab === "changes" && showChanges ? (
        <ChangesView access={access} actions={edit} />
      ) : tab === "sql" ? (
        !atLeast(access.level, "read") ? (
          <Limited access={access}>
            Your access is schema: tables and columns, no rows or SQL.
          </Limited>
        ) : (
          <Suspense
            fallback={<p className="p-5 text-(--g-mut)">Loading the editor</p>}
          >
            <SqlView
              key={sqlDraft?.n}
              tables={tables}
              dialect={engine.dialect}
              db={view.db}
              initialSql={sqlDraft?.sql}
            />
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
          ) : tab === "schema" ? (
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

/**
 * "why" after the caller's access: a popover under it with the steps that
 * decided that access, and the panel's roles. Esc or a click outside
 * closes it.
 */
function WhyLink({ info }: { info: DataInfo }) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (e.target instanceof Node && !box.current?.contains(e.target))
        setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  return (
    <span ref={box} className="relative">
      <button
        type="button"
        className="g-lnk"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        why
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="Your access"
          className="absolute top-full right-0 z-30 mt-2 flex w-[360px] flex-col gap-3 rounded-lg border border-(--g-edge) bg-(--g-panel) p-3 text-left text-xs whitespace-normal text-(--g-dim)"
        >
          <section className="flex flex-col gap-1">
            <h2 className="font-semibold text-(--g-text)">Your access</h2>
            <Why access={info.access} />
          </section>
          <SetupLine info={info} />
        </div>
      )}
    </span>
  );
}

/**
 * Who set up the panel's roles and when, and for those who manage the
 * database, setting them up again with new passwords (`dock db setup`).
 */
function SetupLine({ info }: { info: DataInfo }) {
  const { roles } = useDataSource();
  const setup = useDataSetup();
  const cli = useCli();
  return (
    <section className="flex flex-col items-start gap-1.5 border-t border-(--g-line) pt-3">
      <h2 className="font-semibold text-(--g-text)">Roles</h2>
      <p>
        {roles.reader} and {roles.writer}
        {info.set_up_at
          ? `, set up ${ago(info.set_up_at)}${info.set_up_by ? ` by ${info.set_up_by}` : ""}`
          : ""}
        .
      </p>
      {info.access.manage && (
        <Act
          small
          cli={cli("dock db setup")}
          disabled={setup.isPending}
          onClick={() => setup.mutate()}
        >
          {setup.isPending ? "Rotating" : "Rotate their passwords"}
        </Act>
      )}
      <ErrorText error={setup.error} />
    </section>
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
