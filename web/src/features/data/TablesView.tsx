import { useReducer, useState } from "react";
import type { ServiceRef } from "../../data/types";
import { plural } from "../../ui/format";
import { type TableQuery, useTableRows } from "./api";
import { cellInput, estimate, rowKey, tableName, whereFlag } from "./cells";
import { editsReducer, noEdits, rowId, toChanges } from "./edits";
import { FilterBar } from "./FilterBar";
import { Grid, type GridCol } from "./Grid";
import { type EditActions, ReviewPanel } from "./ReviewPanel";
import { RowDrawer } from "./RowDrawer";
import type { DataFilter, DataInfo, DataTable } from "./types";

/**
 * One table's rows: keyset pages loaded as the grid scrolls, filters and
 * sort, FK links, the selected row's drawer, and (in edit mode) staged
 * cell edits and deletes with their review. Mount with key={table} so
 * staged edits never carry over to another table.
 */
export function TablesView({
  serviceRef,
  info,
  table,
  query,
  editing,
  actions,
  flags,
  onQuery,
}: {
  serviceRef: ServiceRef;
  info: DataInfo;
  table: DataTable;
  query: TableQuery;
  /** Edit mode is on (the panel's header toggles it). */
  editing: boolean;
  actions?: EditActions;
  flags: string;
  /** Show another table, or this one filtered or sorted differently. */
  onQuery: (next: TableQuery) => void;
}) {
  const rows = useTableRows(serviceRef, query);
  const [edits, dispatch] = useReducer(editsReducer, noEdits);
  const [selected, setSelected] = useState<number | null>(null);

  const pages = rows.data?.pages ?? [];
  const columns = pages[0]?.columns ?? [];
  const names = columns.map((c) => c.name);
  const data = pages.flatMap((p) => p.rows ?? []);
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
  const total =
    pages.at(-1)?.exact_rows ??
    pages[0]?.estimated_rows ??
    table.estimated_rows;
  const cli = [
    `dock db rows ${name}`,
    ...filters.map(whereFlag).filter(Boolean),
    query.order && `--order ${query.order}`,
    flags,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="flex min-h-0 flex-1 gap-4">
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h2 className="font-mono text-md font-semibold">{name}</h2>
          <span className="text-mut">
            {filters.length
              ? `${loaded}${rows.hasNextPage ? "+" : ""} matching`
              : estimate(total)}
            {!table.editable &&
              (pk.length ? ", read-only view" : ", no primary key: read-only")}
          </span>
          <span className="ml-auto font-mono text-2xs text-mut">{cli}</span>
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
            environment={serviceRef.environment}
            confirmCommit={info.access.confirm_commit}
            propose={info.access.level === "propose"}
            actions={actions}
            flags={flags}
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
          serviceRef={serviceRef}
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
