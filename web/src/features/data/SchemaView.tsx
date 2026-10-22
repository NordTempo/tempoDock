import type { ReactNode } from "react";
import { Scroll } from "../../ui/kit";
import { useCli } from "./api";
import { byteSize, rowCount, tableName, tableRows } from "./cells";
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
    <div className="flex min-w-0 flex-1 flex-col gap-5 overflow-y-auto">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <h2 className="font-mono text-md font-semibold">{name}</h2>
        <span className="text-mut">
          {kindLabel[table.kind]}, {rowCount(tableRows(table))}
          {table.size_bytes > 0 && `, ${byteSize(table.size_bytes)}`}
          {!table.editable && ", read-only"}
        </span>
        <span className="ml-auto font-mono text-2xs text-mut">
          {cli(`dock db schema ${name}`)}
        </span>
      </div>
      {table.comment && <p className="text-mut">{table.comment}</p>}

      <Scroll>
        <table className="font-mono text-xs">
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
                c.sensitive && "sensitive",
                c.comment,
              ].filter(Boolean);
              return (
                <tr key={c.name}>
                  <td className={pk.includes(c.name) ? "font-semibold" : ""}>
                    {c.name}
                  </td>
                  <td className="text-[#d4d4d4]">{c.type}</td>
                  <td className="text-mut">
                    {c.nullable ? "null" : "not null"}
                  </td>
                  <td className="max-w-64 truncate text-mut" title={c.default}>
                    {c.default}
                  </td>
                  <td className="text-mut">
                    {fk && (
                      <button
                        type="button"
                        className="mr-3 text-prog hover:underline"
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
                    <td>
                      <button
                        type="button"
                        className="lnk"
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
            <Open name={`${fk.ref_schema}.${fk.ref_table}`} onOpen={onOpen} /> (
            {(fk.ref_columns ?? []).join(", ")}){" "}
            <span className="text-faint">{fk.name}</span>
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
            <span className="text-faint">{fk.name}</span>
          </li>
        )}
      </Section>
      <Section title="Indexes" empty="No indexes." items={table.indexes ?? []}>
        {(ix) => (
          <li key={ix.name}>
            <span className="text-[#d4d4d4]">{ix.name}</span>
            {ix.primary ? " primary" : ix.unique ? " unique" : ""}
            <div className="text-mut">{ix.definition}</div>
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
            <span className="text-[#d4d4d4]">{c.name}</span> {c.kind}
            <div className="text-mut">{c.definition}</div>
          </li>
        )}
      </Section>
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
    <button
      type="button"
      className="text-prog hover:underline"
      onClick={() => onOpen(name)}
    >
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
    <section className="flex flex-col gap-1">
      <h3 className="text-sm font-semibold">{title}</h3>
      {items.length ? (
        <ul className="flex flex-col gap-1 font-mono text-xs">
          {items.map(children)}
        </ul>
      ) : (
        <p className="text-xs text-mut">{empty}</p>
      )}
    </section>
  );
}
