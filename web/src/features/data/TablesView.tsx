import { useQuery } from "@tanstack/react-query";
import { useMemo, useReducer, useState } from "react";
import { useShortcuts } from "../../shell/keys";
import { plural } from "../../ui/format";
import { type TableQuery, useCli, useDataSource, useTableRows } from "./api";
import { cellInput, filterFlag, rowCount, rowKey, tableName } from "./cells";
import { editsReducer, noEdits, rowId, toChanges } from "./edits";
import { FilterBar } from "./FilterBar";
import { Grid, type GridCol } from "./Grid";
import { Btn, Head, Kbd } from "./kit";
import { type EditActions, ReviewPanel } from "./ReviewPanel";
import { RowDrawer } from "./RowDrawer";
import type { DataFilter, DataInfo, DataTable } from "./types";

/**
 * One table's rows: keyset pages loaded as the grid scrolls, filters and
 * sort, FK links, the selected row's drawer, per-column reveal of
 * sensitive columns, and (in edit mode) staged cell edits and deletes with
 * their review in the side pane (G1, G4). Mount with key={table} so staged edits and reveals never
 * carry over to another table.
 */
export function TablesView({
  info,
  table,
  query,
  editing,
  onEditing,
  actions,
  onQuery,
}: {
  info: DataInfo;
  table: DataTable;
  query: TableQuery;
  /** Edit mode is on. */
  editing: boolean;
  /** Turns edit mode on or off; absent when the caller can't edit. */
  onEditing?: (editing: boolean) => void;
  actions?: EditActions;
  /** Show another table, or this one filtered or sorted differently. */
  onQuery: (next: TableQuery) => void;
}) {
  // Revealed sensitive columns, for callers who may see them. Like edit
  // mode it's per mount and never remembered.
  const [revealed, setRevealed] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const cli = useCli();
  const rows = useTableRows({
    ...query,
    reveal: revealed.size > 0,
  });
  const [edits, dispatch] = useReducer(editsReducer, noEdits);
  const [selected, setSelected] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  // The new-row form, open in edit mode.
  const [addingRow, setAddingRow] = useState(false);
  // An exact count of the rows matching, asked for (`dock db rows --count`).
  const [counting, setCounting] = useState(false);
  const source = useDataSource();
  const exact = useQuery({
    queryKey: [...source.key, "rows", query.table, "count", query.filters],
    queryFn: ({ signal }) =>
      source.rows(
        { table: query.table, filters: query.filters, limit: 1, count: true },
        signal,
      ),
    enabled: counting,
    staleTime: Number.POSITIVE_INFINITY,
  });
  useShortcuts([
    { key: "/", label: "filter rows", run: () => setAdding(true) },
  ]);

  const pages = rows.data?.pages ?? [];
  const columns = pages[0]?.columns ?? [];
  const names = columns.map((c) => c.name);
  const sensitive = (table.columns ?? [])
    .filter((c) => c.sensitive)
    .map((c) => c.name);
  const canReveal = info.access.sensitive === "visible";
  // A reveal fetches all of the table's sensitive values; the columns not
  // revealed yet stay masked on screen.
  const data = useMemo(() => {
    const all = (rows.data?.pages ?? []).flatMap((p) => p.rows ?? []);
    const cols = rows.data?.pages[0]?.columns ?? [];
    const hide = cols.flatMap((c, i) =>
      revealed.size > 0 &&
      !revealed.has(c.name) &&
      table.columns?.some((t) => t.name === c.name && t.sensitive)
        ? [i]
        : [],
    );
    if (!hide.length) return all;
    return all.map((r) => {
      const masked = r.slice();
      for (const i of hide) masked[i] = { masked: true };
      return masked;
    });
  }, [rows.data, revealed, table]);
  const toggleReveal = (column: string) =>
    setRevealed((prev) => {
      const next = new Set(prev);
      if (!next.delete(column)) next.add(column);
      return next;
    });
  const name = tableName(table);
  const filters = query.filters ?? [];
  const meta = new Map((table.columns ?? []).map((c) => [c.name, c]));
  const pk = table.primary_key ?? [];
  const singleFk = (column: string) =>
    (table.foreign_keys ?? []).find(
      (fk) => fk.columns?.length === 1 && fk.columns[0] === column,
    );
  const canEdit = editing && table.editable && !!actions;
  const changes = toChanges(name, edits);
  const selectedRow = selected === null ? undefined : data[selected];
  const selectedKey = selected === null ? null : keyAtRow(selected);
  const selectedDeleted = !!selectedKey && !!edits[rowId(selectedKey)]?.deleted;
  function keyAtRow(row: number) {
    const r = data[row];
    return r ? rowKey(table, names, r) : null;
  }
  const keyAt = keyAtRow;

  const gridColumns: GridCol[] = columns.map((c) => ({
    ...c,
    link: !!singleFk(c.name),
    sort:
      query.order === c.name
        ? "↑"
        : query.order === `-${c.name}`
          ? "↓"
          : undefined,
    editable: canEdit && !pk.includes(c.name) && !meta.get(c.name)?.generated,
  }));

  const follow = (row: number, column: string) => {
    const fk = singleFk(column);
    const value = cellInput(data[row]?.[names.indexOf(column)] ?? null);
    if (!fk || typeof value !== "string") return;
    onQuery({
      table: `${fk.ref_schema}.${fk.ref_table}`,
      filters: [{ column: fk.ref_columns?.[0] ?? "", op: "eq", value }],
    });
  };

  const sortBy = (column: string) =>
    onQuery({
      ...query,
      order:
        query.order === column
          ? `-${column}`
          : query.order === `-${column}`
            ? undefined
            : column,
    });

  const loaded = data.length;
  // An exact count asked for wins, then the schema's count of a
  // never-analysed table, then the estimate.
  const total = {
    estimated: pages[0]?.estimated_rows ?? table.estimated_rows,
    exact: pages.at(-1)?.exact_rows ?? table.exact_rows,
  };
  const rowsCli = cli(
    [
      `dock db rows ${name}`,
      ...filters.map(filterFlag).filter(Boolean),
      query.order && `--order ${query.order}`,
      revealed.size > 0 && "--reveal",
      counting && "--count",
    ]
      .filter(Boolean)
      .join(" "),
  );

  const counted = exact.data?.exact_rows;
  const count =
    typeof counted === "number"
      ? `${plural(counted, "row")}${filters.length ? " matching" : ""}`
      : filters.length
        ? `${loaded}${rows.hasNextPage ? "+" : ""} matching`
        : rowCount(total);
  // Worth counting: more rows than loaded, and no exact count yet.
  const canCount =
    typeof counted !== "number" &&
    rows.hasNextPage &&
    (filters.length > 0 || typeof total.exact !== "number");
  const review = canEdit && actions && changes.length > 0;

  return (
    <div className="flex min-h-0 min-w-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <Head
          crumb={table.schema}
          title={table.name}
          actions={
            <>
              <span className="mr-2 hidden font-mono text-2xs text-(--g-mut) xl:inline">
                {rowsCli}
              </span>
              <Btn aria-pressed={adding} onClick={() => setAdding(!adding)}>
                Filter <Kbd>/</Kbd>
              </Btn>
              {/* Edit mode's row actions, beside the keys that do the same. */}
              {canEdit && (
                <>
                  <Btn
                    aria-pressed={addingRow}
                    onClick={() => setAddingRow(!addingRow)}
                  >
                    Add row
                  </Btn>
                  <Btn
                    disabled={selectedKey === null}
                    title={
                      selectedKey === null
                        ? "Select a row first"
                        : "Or press Delete"
                    }
                    onClick={() =>
                      selectedKey &&
                      dispatch({
                        type: selectedDeleted ? "restore" : "delete",
                        key: selectedKey,
                      })
                    }
                  >
                    {selectedDeleted ? "Restore row" : "Delete row"}
                  </Btn>
                </>
              )}
              {onEditing && table.editable && (
                <Btn aria-pressed={editing} onClick={() => onEditing(!editing)}>
                  {editing ? "Editing" : "Edit"} <Kbd>E</Kbd>
                </Btn>
              )}
            </>
          }
        >
          <span className="text-xs text-(--g-mut)">
            {count}
            {canCount && (
              <>
                {" "}
                <button
                  type="button"
                  className="g-lnk"
                  disabled={exact.isFetching}
                  title={cli(`dock db rows ${name} --count`)}
                  onClick={() => setCounting(true)}
                >
                  {exact.isFetching ? "counting" : "count"}
                </button>
              </>
            )}
            {exact.error && (
              <span className="text-(--g-fail)"> {exact.error.message}</span>
            )}
            {!table.editable &&
              (pk.length ? ", a view" : ", no primary key: can't be edited")}
          </span>
        </Head>
        <FilterBar
          columns={table.columns ?? []}
          filters={filters}
          order={query.order}
          adding={adding}
          onAdding={setAdding}
          onFilters={(next: DataFilter[]) =>
            onQuery({ ...query, filters: next })
          }
          onOrder={(order) => onQuery({ ...query, order })}
        />
        {sensitive.length > 0 && (
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1 px-5 pt-3 text-xs text-(--g-mut)">
            <span>Sensitive</span>
            {sensitive.map((c) => (
              <span key={c} className="inline-flex items-center gap-1.5">
                <span className="font-mono text-(--g-code)">{c}</span>
                {canReveal ? (
                  <button
                    type="button"
                    className="g-lnk"
                    aria-pressed={revealed.has(c)}
                    onClick={() => toggleReveal(c)}
                  >
                    {revealed.has(c) ? "Hide" : "Reveal"}
                  </button>
                ) : (
                  "masked"
                )}
              </span>
            ))}
            <span>
              {canReveal ? "each reveal is audited" : "your access masks them"}
            </span>
          </p>
        )}
        {editing && canEdit && (
          <p className="px-5 pt-3 text-xs text-(--g-mut)">
            Double-click a cell to change it. Nothing is written until you
            commit.
          </p>
        )}
        {canEdit && addingRow && (
          <AddRow
            columns={(table.columns ?? []).filter((c) => !c.generated)}
            onAdd={(values) => {
              dispatch({ type: "insert", values });
              setAddingRow(false);
            }}
            onCancel={() => setAddingRow(false)}
          />
        )}
        <div className="relative mt-3 min-h-[300px] flex-1 px-3">
          {rows.error ? (
            <p className="px-2 text-(--g-fail)">{rows.error.message}</p>
          ) : rows.isPending ? (
            <p className="px-2 text-(--g-mut)">Loading {name}</p>
          ) : (
            <Grid
              columns={gridColumns}
              rows={data}
              selected={selected}
              staged={(row, column) => {
                const key = keyAt(row);
                const edit = key ? edits[rowId(key)] : undefined;
                return edit && column in edit.new
                  ? edit.new[column]
                  : undefined;
              }}
              deleted={(row) => {
                const key = keyAt(row);
                return !!key && !!edits[rowId(key)]?.deleted;
              }}
              onSelectRow={setSelected}
              onSort={sortBy}
              onFollow={follow}
              onEdit={
                canEdit
                  ? (row, column, value) => {
                      const key = keyAt(row);
                      const old = cellInput(
                        data[row]?.[names.indexOf(column)] ?? null,
                      );
                      if (key && old !== undefined) {
                        dispatch({ type: "set", key, column, old, value });
                      }
                    }
                  : undefined
              }
              onDeleteRow={
                canEdit
                  ? (row) => {
                      const key = keyAt(row);
                      if (key) dispatch({ type: "delete", key });
                    }
                  : undefined
              }
              onNearEnd={() => {
                if (rows.hasNextPage && !rows.isFetchingNextPage)
                  rows.fetchNextPage();
              }}
            />
          )}
        </div>
        <p className="flex justify-between px-5 py-2.5 text-xs text-(--g-mut)">
          <span>
            {rows.isFetchingNextPage
              ? `${plural(loaded, "row")}, loading the next page`
              : rows.hasNextPage
                ? `${plural(loaded, "row")} loaded, more as you scroll`
                : rows.isSuccess && `all ${plural(loaded, "row")}`}
          </span>
          {editing && !table.editable && (
            <span>This table can't be edited</span>
          )}
        </p>
      </div>
      {review ? (
        <ReviewPanel
          table={table.name}
          body={{ changes }}
          // A proposal writes nothing: its approver confirms instead.
          confirmCommit={
            info.access.confirm_commit && info.access.level !== "propose"
          }
          propose={info.access.level === "propose"}
          actions={actions}
          onRestore={(key) => dispatch({ type: "restore", key })}
          onDiscard={() => dispatch({ type: "discard" })}
          onCommitted={() => {
            dispatch({ type: "discard" });
            rows.refetch();
          }}
        />
      ) : (
        selectedRow && (
          <RowDrawer
            table={table}
            columns={columns}
            row={selectedRow}
            onOpen={(target, next) => onQuery({ table: target, filters: next })}
            onClose={() => setSelected(null)}
          />
        )
      )}
    </div>
  );
}

/**
 * A new row's values, one field per column that isn't generated. An empty
 * field is left out, so the column's default (or NULL) applies. Staged
 * like other edits: it shows in the review pane until committed.
 */
function AddRow({
  columns,
  onAdd,
  onCancel,
}: {
  columns: DataTable["columns"];
  onAdd: (values: Record<string, string>) => void;
  onCancel: () => void;
}) {
  const [values, setValues] = useState<Record<string, string>>({});
  return (
    <form
      aria-label="Add a row"
      className="mx-5 mt-3 flex flex-col gap-2 rounded-lg border border-(--g-line) p-3"
      onSubmit={(e) => {
        e.preventDefault();
        onAdd(
          Object.fromEntries(
            Object.entries(values).filter(([, v]) => v !== ""),
          ),
        );
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          onCancel();
        }
      }}
    >
      <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-2">
        {(columns ?? []).map((c, i) => (
          <label key={c.name} className="flex flex-col gap-1 text-xs">
            <span className="text-(--g-mut)">
              <span className="font-mono text-(--g-text)">{c.name}</span>{" "}
              {c.type_name}
            </span>
            <input
              // biome-ignore lint/a11y/noAutofocus: the form opens on request
              autoFocus={i === 0}
              value={values[c.name] ?? ""}
              placeholder={
                c.default
                  ? `default ${c.default}`
                  : c.nullable
                    ? "NULL"
                    : "required"
              }
              onChange={(e) =>
                setValues({ ...values, [c.name]: e.target.value })
              }
              className="g-field h-7 font-mono text-xs"
            />
          </label>
        ))}
      </div>
      <div className="flex items-center justify-end gap-2">
        <Btn onClick={onCancel}>Cancel</Btn>
        <Btn primary type="submit">
          Stage row
        </Btn>
      </div>
    </form>
  );
}
