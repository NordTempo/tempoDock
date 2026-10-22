import { useKeydown } from "../../shell/keys";
import { useReferences } from "./api";
import {
  cellDetail,
  cellInput,
  cellKind,
  cellText,
  rowKey,
  tableName,
} from "./cells";
import type { DataFilter, DataResultColumn, DataRow, DataTable } from "./types";

/**
 * The selected row: every value in full, its outgoing foreign keys as
 * links, and the rows that reference it (incoming foreign keys). Esc closes.
 */
export function RowDrawer({
  table,
  columns,
  row,
  onOpen,
  onClose,
}: {
  table: DataTable;
  columns: readonly DataResultColumn[];
  row: DataRow;
  /** Open a table filtered to some rows (FK navigation). */
  onOpen: (table: string, filters: DataFilter[]) => void;
  onClose: () => void;
}) {
  const names = columns.map((c) => c.name);
  const key = rowKey(table, names, row);
  const refs = useReferences(tableName(table), key);
  useKeydown((e) => e.key === "Escape" && onClose());

  const fkFor = (column: string) =>
    (table.foreign_keys ?? []).find(
      (fk) => fk.columns?.length === 1 && fk.columns[0] === column,
    );

  return (
    <aside
      aria-label="Row"
      className="flex min-h-0 w-[340px] shrink-0 flex-col gap-3 overflow-y-auto border-l border-line pl-4"
    >
      <div className="flex items-baseline gap-2">
        <h2 className="text-md font-semibold">Row</h2>
        <span className="truncate font-mono text-xs text-mut">
          {key
            ? Object.entries(key)
                .map(([k, v]) => `${k} = ${v}`)
                .join(", ")
            : "no primary key"}
        </span>
        <button type="button" className="lnk ml-auto" onClick={onClose}>
          close <kbd>Esc</kbd>
        </button>
      </div>

      <dl className="grid grid-cols-[minmax(0,110px)_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-xs">
        {columns.map((c, i) => {
          const cell = row[i] ?? null;
          const kind = cellKind(cell, c.type_name);
          const fk = fkFor(c.name);
          const value = cellInput(cell);
          return (
            <div key={c.name} className="contents">
              <dt
                className="truncate text-mut"
                title={`${c.name} ${c.type_name}`}
              >
                {c.name}
              </dt>
              <dd className="min-w-0">
                {fk && typeof value === "string" ? (
                  <button
                    type="button"
                    className="text-left text-prog hover:underline"
                    onClick={() =>
                      onOpen(`${fk.ref_schema}.${fk.ref_table}`, [
                        { column: fk.ref_columns?.[0] ?? "", op: "eq", value },
                      ])
                    }
                  >
                    {value} → {fk.ref_table}
                  </button>
                ) : (
                  <span
                    className={`block max-h-40 overflow-y-auto break-all whitespace-pre-wrap ${
                      kind === "null" || kind === "masked" ? "text-faint" : ""
                    }`}
                  >
                    {cellDetail(cell, c.type_name)}
                  </span>
                )}
              </dd>
            </div>
          );
        })}
      </dl>

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">Referenced by</h3>
        {!key && (
          <p className="text-xs text-mut">
            Rows without a primary key can't be referenced.
          </p>
        )}
        {refs.isPending && key && (
          <p className="text-xs text-mut">Loading references</p>
        )}
        {refs.error && (
          <p className="text-xs text-fail">{refs.error.message}</p>
        )}
        {refs.data?.length === 0 && (
          <p className="text-xs text-mut">Nothing references this row.</p>
        )}
        {refs.data?.map((r) => {
          const rows = r.page.rows ?? [];
          const cols = (r.page.columns ?? []).slice(0, 4);
          const target = `${r.schema}.${r.table}`;
          return (
            <div key={r.constraint} className="font-mono text-xs">
              <div className="flex items-baseline gap-2">
                <button
                  type="button"
                  className="text-prog hover:underline"
                  onClick={() => onOpen(target, r.filters ?? [])}
                  title={r.constraint}
                >
                  {r.table}.{(r.columns ?? []).join(", ")} →
                </button>
                <span className="text-mut">
                  {r.page.next
                    ? `${rows.length}+ rows`
                    : `${rows.length} ${rows.length === 1 ? "row" : "rows"}`}
                </span>
              </div>
              {rows.length > 0 && (
                <table className="mt-1">
                  <tbody>
                    {rows.slice(0, 5).map((row, i) => (
                      <tr key={i}>
                        {cols.map((c, j) => (
                          <td
                            key={c.name}
                            className="py-0.5 pr-3 text-[#d4d4d4]"
                          >
                            {cellText(row[j] ?? null, c.type_name)}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          );
        })}
      </section>
    </aside>
  );
}
