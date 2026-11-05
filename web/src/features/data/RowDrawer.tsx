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

  // "orders · id 4", or "orders" and why a row without a key is plain.
  const keyText = key
    ? Object.entries(key)
        .map(([k, v]) => `${k} ${v}`)
        .join(", ")
    : null;

  return (
    <aside
      aria-label="Row details"
      className="g-aside w-[300px] shrink-0 basis-[300px]"
    >
      <div className="flex h-7 items-center justify-between text-xs text-(--g-mut)">
        <span>Row details</span>
        <button
          type="button"
          aria-label="Close row details"
          className="g-btn ghost sm"
          onClick={onClose}
        >
          <span className="g-kbd">Esc</span>
        </button>
      </div>
      <h2
        className="mt-1 mb-3 truncate text-[15px] font-semibold"
        title={keyText ?? undefined}
      >
        {table.name}
        {keyText && (
          <span className="font-normal text-(--g-dim)"> · {keyText}</span>
        )}
      </h2>
      {!key && (
        <p className="-mt-2 mb-3 text-xs text-(--g-mut)">
          No primary key, so nothing can reference this row.
        </p>
      )}

      <dl className="grid grid-cols-[110px_minmax(0,1fr)] gap-x-3">
        {columns.map((c, i) => {
          const cell = row[i] ?? null;
          const kind = cellKind(cell, c.type_name);
          const fk = fkFor(c.name);
          const value = cellInput(cell);
          return (
            <div key={c.name} className="contents">
              <dt
                className="truncate py-1.5 text-(--g-mut)"
                title={`${c.name} ${c.type_name}`}
              >
                {c.name}
              </dt>
              <dd className="min-w-0 py-1.5 font-mono text-xs leading-5">
                {fk && typeof value === "string" ? (
                  <button
                    type="button"
                    className="g-lnk text-left"
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
                      kind === "null" || kind === "masked"
                        ? "text-(--g-faint)"
                        : ""
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

      <section className="mt-4 flex flex-col">
        <h3 className="g-label mb-1">Connected</h3>
        {refs.isPending && key && (
          <p className="text-xs text-(--g-mut)">Loading references</p>
        )}
        {refs.error && (
          <p className="text-xs text-(--g-fail)">{refs.error.message}</p>
        )}
        {(!key || refs.data?.length === 0) && (
          <p className="text-xs text-(--g-mut)">Nothing references this row.</p>
        )}
        {refs.data?.map((r) => {
          const rows = r.page.rows ?? [];
          const cols = (r.page.columns ?? []).slice(0, 3);
          const target = `${r.schema}.${r.table}`;
          return (
            <div key={r.constraint} className="flex flex-col">
              <button
                type="button"
                className="g-item -mx-2 w-auto text-(--g-text)"
                onClick={() => onOpen(target, r.filters ?? [])}
                title={`${r.table}.${(r.columns ?? []).join(", ")} (${r.constraint})`}
              >
                <span className="flex-1 truncate">{r.table}</span>
                <span className="text-xs text-(--g-mut)">
                  {r.page.next ? `${rows.length}+` : rows.length}
                </span>
              </button>
              {rows.slice(0, 3).map((row, i) => (
                <span
                  key={i}
                  className="truncate pl-2 font-mono text-2xs text-(--g-mut)"
                >
                  {cols
                    .map((c, j) => cellText(row[j] ?? null, c.type_name))
                    .join(" · ")}
                </span>
              ))}
            </div>
          );
        })}
      </section>
    </aside>
  );
}
