import { useMemo, useReducer, useState } from "react";
import { plural } from "../../ui/format";
import { type TableQuery, useCli, useTableRows } from "./api";
import { cellInput, filterFlag, rowCount, rowKey, tableName } from "./cells";
import { editsReducer, noEdits, rowId, toChanges } from "./edits";
import { FilterBar } from "./FilterBar";
import { Grid, type GridCol } from "./Grid";
import { type EditActions, ReviewPanel } from "./ReviewPanel";
import { RowDrawer } from "./RowDrawer";
import type { DataFilter, DataInfo, DataTable } from "./types";

/**
 * One table's rows: keyset pages loaded as the grid scrolls, filters and
 * sort, FK links, the selected row's drawer, per-column reveal of
 * sensitive columns, and (in edit mode) staged cell edits and deletes with
 * their review. Mount with key={table} so staged edits and reveals never
 * carry over to another table.
 */
export function TablesView({
  info,
  table,
  query,
  editing,
  actions,
  onQuery,
}: {
  info: DataInfo;
  table: DataTable;
  query: TableQuery;
  /** Edit mode is on (the panel's header toggles it). */
  editing: boolean;
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
  const keyAt = (row: number) => {
    const r = data[row];
    return r ? rowKey(table, names, r) : null;
  };

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
    ]
      .filter(Boolean)
      .join(" "),
  );

  return (
    <div className="flex min-h-0 flex-1 gap-4">
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h2 className="font-mono text-md font-semibold">{name}</h2>
          <span className="text-mut">
            {filters.length
              ? `${loaded}${rows.hasNextPage ? "+" : ""} matching`
              : rowCount(total)}
            {!table.editable &&
              (pk.length ? ", read-only view" : ", no primary key: read-only")}
          </span>
          <span className="ml-auto font-mono text-2xs text-mut">{rowsCli}</span>
        </div>
        <FilterBar
          columns={table.columns ?? []}
          filters={filters}
          order={query.order}
          onFilters={(next: DataFilter[]) =>
            onQuery({ ...query, filters: next })
          }
          onOrder={(order) => onQuery({ ...query, order })}
        />
        {sensitive.length > 0 && (
          <p className="flex flex-wrap gap-x-3 font-mono text-2xs text-mut">
            <span>sensitive</span>
            {sensitive.map((c) => (
              <span key={c}>
                <span className="text-[#d4d4d4]">{c}</span>{" "}
                {canReveal ? (
                  <button
                    type="button"
                    className="lnk"
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
              {canReveal
                ? "· each reveal is audited (data.reveal)"
                : "· your access masks them"}
            </span>
          </p>
        )}
        <div className="relative min-h-[300px] flex-1 border-t border-line">
          {rows.error ? (
            <p className="pt-3 text-fail">{rows.error.message}</p>
          ) : rows.isPending ? (
            <p className="pt-3 text-mut">Loading {name}</p>
          ) : (
            <Grid
              columns={gridColumns}
              rows={data}
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
        <p className="font-mono text-2xs text-mut">
          {rows.isFetchingNextPage
            ? `${plural(loaded, "row")}, loading the next page`
            : rows.hasNextPage
              ? `${plural(loaded, "row")} loaded, more as you scroll`
              : rows.isSuccess && `all ${plural(loaded, "row")}`}
          {canEdit && " · double-click a cell to edit, Delete removes its row"}
          {editing && !table.editable && " · this table can't be edited"}
        </p>
        {canEdit && actions && changes.length > 0 && (
          <ReviewPanel
            changes={changes}
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
        )}
      </div>
      {selectedRow && (
        <RowDrawer
          table={table}
          columns={columns}
          row={selectedRow}
          onOpen={(target, next) => onQuery({ table: target, filters: next })}
          onClose={() => setSelected(null)}
        />
      )}
    </div>
  );
}
