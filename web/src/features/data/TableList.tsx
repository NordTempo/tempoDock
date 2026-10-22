import { useRef, useState } from "react";
import { useShortcuts } from "../../shell/keys";
import { compactCount, tableName, tableRows } from "./cells";
import type { DataTable } from "./types";

const kindMark: Partial<Record<DataTable["kind"], string>> = {
  view: "view",
  materialized_view: "mat view",
  foreign_table: "foreign",
  partitioned_table: "partitioned",
};

/**
 * The schema sidebar: tables grouped by schema with row counts, and a
 * filter (`/`). `selected` and `onSelect` use "schema.name".
 */
export function TableList({
  tables,
  selected,
  onSelect,
}: {
  tables: readonly DataTable[];
  selected?: string;
  onSelect: (table: string) => void;
}) {
  const [filter, setFilter] = useState("");
  const input = useRef<HTMLInputElement>(null);
  useShortcuts([
    { key: "/", label: "find table", run: () => input.current?.focus() },
  ]);

  const needle = filter.trim().toLowerCase();
  const shown = needle
    ? tables.filter((t) => tableName(t).toLowerCase().includes(needle))
    : tables;
  const schemas = new Map<string, DataTable[]>();
  for (const t of shown) {
    schemas.set(t.schema, [...(schemas.get(t.schema) ?? []), t]);
  }

  return (
    <nav aria-label="Tables" className="flex min-h-0 flex-col gap-2">
      <input
        ref={input}
        className="field py-1 font-mono text-xs"
        placeholder="Find table  /"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            setFilter("");
            e.currentTarget.blur();
          } else if (e.key === "Enter" && shown[0]) {
            onSelect(tableName(shown[0]));
            e.currentTarget.blur();
          }
        }}
      />
      <div className="min-h-0 overflow-y-auto font-mono text-xs">
        {[...schemas].map(([schema, list]) => (
          <section key={schema} className="mb-2">
            <h3 className="py-0.5 text-mut">{schema}</h3>
            <ul>
              {list.map((t) => {
                const name = tableName(t);
                return (
                  <li key={name}>
                    <button
                      type="button"
                      aria-current={name === selected ? "true" : undefined}
                      onClick={() => onSelect(name)}
                      title={t.comment || name}
                      className={`flex w-full items-baseline gap-2 py-0.5 pl-2.5 text-left hover:bg-hover ${
                        name === selected
                          ? "bg-sel text-white"
                          : "text-[#d4d4d4]"
                      }`}
                    >
                      <span className="truncate">{t.name}</span>
                      {kindMark[t.kind] && (
                        <span className="text-faint">{kindMark[t.kind]}</span>
                      )}
                      <span className="ml-auto pr-1.5 text-faint">
                        {compactCount(tableRows(t))}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        ))}
        {!shown.length && <p className="text-mut">No table matches.</p>}
      </div>
    </nav>
  );
}
