import { useState } from "react";
import { compactCount, tableName, tableRows } from "./cells";
import type { DataTable } from "./types";

const kindMark: Partial<Record<DataTable["kind"], string>> = {
  view: "view",
  materialized_view: "mat view",
  foreign_table: "foreign",
  partitioned_table: "partitioned",
};

/**
 * The table list: tables grouped by schema with row counts, and a filter
 * (`/`). In the panel's left column in Dock, in the sidebar in tempoBase. `selected` and `onSelect` use "schema.name".
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
        className="g-field h-8"
        placeholder="Find table  /"
        aria-label="Find table"
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
      <div className="min-h-0 overflow-y-auto">
        {[...schemas].map(([schema, list]) => (
          <section key={schema} className="mb-2">
            {schemas.size > 1 || schema !== "public" ? (
              <h3 className="g-label px-2.5 pt-1.5 pb-1">{schema}</h3>
            ) : null}
            <ul className="flex flex-col gap-px">
              {list.map((t) => {
                const name = tableName(t);
                return (
                  <li key={name}>
                    <button
                      type="button"
                      aria-current={name === selected ? "true" : undefined}
                      onClick={() => onSelect(name)}
                      title={t.comment || name}
                      className="g-item"
                    >
                      <span className="truncate">{t.name}</span>
                      {kindMark[t.kind] && (
                        <span className="text-2xs text-(--g-faint)">
                          {kindMark[t.kind]}
                        </span>
                      )}
                      <span className="ml-auto text-2xs text-(--g-mut)">
                        {compactCount(tableRows(t))}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        ))}
        {!shown.length && (
          <p className="px-2.5 text-(--g-mut)">No table matches.</p>
        )}
      </div>
    </nav>
  );
}
