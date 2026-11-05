import type { ReactNode } from "react";
import { useCli } from "./api";
import { byteSize, rowCount, tableName, tableRows } from "./cells";
import { Head, Scroll } from "./kit";
import { SensitiveSection, useSensitiveMarks } from "./SensitiveColumns";
import type { DataTable } from "./types";

const kindLabel: Record<DataTable["kind"], string> = {
  table: "table",
  partitioned_table: "partitioned table",
  view: "view",
  materialized_view: "materialized view",
  foreign_table: "foreign table",
};

/**
 * One table's structure: columns with keys, foreign keys both ways,
 * indexes and constraints. `tables` is the whole schema, for the
 * tables that reference this one. Admins (`manage`) also mark and unmark
 * sensitive columns here.
 */
export function SchemaView({
  table,
  tables,
  manage,
  onOpen,
}: {
  table: DataTable;
  tables: readonly DataTable[];
  /** The caller may mark sensitive columns (admins). */
  manage: boolean;
  /** Show another table's structure. */
  onOpen: (table: string) => void;
}) {
  const marks = useSensitiveMarks(tables);
  const cli = useCli();
  const name = tableName(table);
  const pk = table.primary_key ?? [];
  const fks = table.foreign_keys ?? [];
  const incoming = tables.flatMap((t) =>
    (t.foreign_keys ?? [])
      .filter((fk) => `${fk.ref_schema}.${fk.ref_table}` === name)
      .map((fk) => ({ from: t, fk })),
  );
  const fkOf = (column: string) =>
    fks.find((fk) => fk.columns?.includes(column));

  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-y-auto">
      <Head
        crumb={table.schema}
        title={table.name}
        actions={
          <span className="font-mono text-2xs text-(--g-mut)">
            {cli(`dock db schema ${name}`)}
          </span>
        }
      >
        <span className="text-xs text-(--g-mut)">
          {kindLabel[table.kind]} · {rowCount(tableRows(table))}
          {table.size_bytes > 0 && ` · ${byteSize(table.size_bytes)}`}
          {!table.editable && " · read only"}
        </span>
      </Head>
      <div className="flex flex-col gap-6 px-3 py-3">
        {table.comment && (
          <p className="px-2 text-(--g-dim)">{table.comment}</p>
        )}

        <Scroll>
          <table className="g-table">
            <thead>
              <tr>
                <th>column</th>
                <th>type</th>
                <th>null</th>
                <th>default</th>
                <th>notes</th>
                {manage && <th>sensitive</th>}
              </tr>
            </thead>
            <tbody>
              {(table.columns ?? []).map((c) => {
                const fk = fkOf(c.name);
                const notes = [
                  pk.includes(c.name) && "primary key",
                  c.generated && "generated",
                  c.sensitive && !manage && "sensitive",
                  c.comment,
                ].filter(Boolean);
                return (
                  <tr key={c.name} className="group">
                    <td className={pk.includes(c.name) ? "font-semibold" : ""}>
                      {c.name}
                    </td>
                    <td className="font-mono text-xs text-(--g-dim)">
                      {c.type}
                    </td>
                    <td className="text-xs text-(--g-mut)">
                      {c.nullable ? "null" : "not null"}
                    </td>
                    <td
                      className="max-w-64 truncate font-mono text-xs text-(--g-mut)"
                      title={c.default}
                    >
                      {c.default}
                    </td>
                    <td className="text-xs text-(--g-mut)">
                      {fk && (
                        <button
                          type="button"
                          className="g-lnk mr-3"
                          onClick={() =>
                            onOpen(`${fk.ref_schema}.${fk.ref_table}`)
                          }
                        >
                          → {fk.ref_table}.{(fk.ref_columns ?? []).join(", ")}
                        </button>
                      )}
                      {notes.join(", ")}
                    </td>
                    {manage && (
                      <td className="text-xs">
                        <span
                          className={
                            c.sensitive ? "text-(--g-warn)" : "text-(--g-faint)"
                          }
                        >
                          {c.sensitive ? "masked" : "no"}
                        </span>{" "}
                        <button
                          type="button"
                          className="g-lnk invisible group-hover:visible focus-visible:visible"
                          disabled={marks.pending}
                          onClick={() =>
                            marks.toggle(
                              {
                                schema: table.schema,
                                table: table.name,
                                column: c.name,
                              },
                              !c.sensitive,
                            )
                          }
                        >
                          {c.sensitive ? "Unmark" : "Mark"}
                        </button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </Scroll>
        {manage && <SensitiveSection tables={tables} marks={marks} />}

        <Section title="Foreign keys" empty="No foreign keys." items={fks}>
          {(fk) => (
            <li key={fk.name}>
              ({(fk.columns ?? []).join(", ")}) →{" "}
              <Open name={`${fk.ref_schema}.${fk.ref_table}`} onOpen={onOpen} />{" "}
              ({(fk.ref_columns ?? []).join(", ")}){" "}
              <span className="text-(--g-faint)">{fk.name}</span>
            </li>
          )}
        </Section>
        <Section
          title="Referenced by"
          empty="Nothing references this table."
          items={incoming}
        >
          {({ from, fk }) => (
            <li key={`${tableName(from)}${fk.name}`}>
              <Open name={tableName(from)} onOpen={onOpen} /> (
              {(fk.columns ?? []).join(", ")}) → (
              {(fk.ref_columns ?? []).join(", ")}){" "}
              <span className="text-(--g-faint)">{fk.name}</span>
            </li>
          )}
        </Section>
        <Section
          title="Indexes"
          empty="No indexes."
          items={table.indexes ?? []}
        >
          {(ix) => (
            <li key={ix.name}>
              <span className="text-(--g-code)">{ix.name}</span>
              {ix.primary ? " primary" : ix.unique ? " unique" : ""}
              <div className="text-(--g-mut)">{ix.definition}</div>
            </li>
          )}
        </Section>
        <Section
          title="Constraints"
          empty="No check, unique or exclusion constraints."
          items={table.constraints ?? []}
        >
          {(c) => (
            <li key={c.name}>
              <span className="text-(--g-code)">{c.name}</span> {c.kind}
              <div className="text-(--g-mut)">{c.definition}</div>
            </li>
          )}
        </Section>
      </div>
    </div>
  );
}

function Open({
  name,
  onOpen,
}: {
  name: string;
  onOpen: (table: string) => void;
}) {
  return (
    <button type="button" className="g-lnk" onClick={() => onOpen(name)}>
      {name}
    </button>
  );
}

function Section<T>({
  title,
  empty,
  items,
  children,
}: {
  title: string;
  empty: string;
  items: readonly T[];
  children: (item: T) => ReactNode;
}) {
  return (
    <section className="flex flex-col gap-1.5 px-2">
      <h3 className="text-[13px] font-semibold">{title}</h3>
      {items.length ? (
        <ul className="flex flex-col gap-1.5 font-mono text-xs">
          {items.map(children)}
        </ul>
      ) : (
        <p className="text-xs text-(--g-mut)">{empty}</p>
      )}
    </section>
  );
}
