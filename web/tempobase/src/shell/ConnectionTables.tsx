import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo } from "react";
import {
  DataSourceContext,
  useDataInfo,
  useDataSchema,
} from "../../../src/features/data/api";
import { tableName } from "../../../src/features/data/cells";
import { TableList } from "../../../src/features/data/TableList";
import { connectionSource } from "../data/source";

/**
 * The open connection's tables in the sidebar (G1), in place of the
 * panel's own list (DataPanel tableList={false}). Choosing one shows it
 * in the Tables tab, or the Schema tab when that is open.
 */
export default function ConnectionTables({
  connection,
}: {
  connection: string;
}) {
  const source = useMemo(() => connectionSource(connection), [connection]);
  return (
    <DataSourceContext value={source}>
      <Tables connection={connection} />
    </DataSourceContext>
  );
}

function Tables({ connection }: { connection: string }) {
  const info = useDataInfo();
  const ready =
    info.data?.status === "ready" && info.data.access.level !== "none";
  const schema = useDataSchema(ready);
  const search = useSearch({ strict: false });
  const navigate = useNavigate();
  const tables = schema.data?.tables ?? [];
  if (!tables.length) return null;
  const selected = search.table ?? (tables[0] && tableName(tables[0]));
  return (
    <div className="flex max-h-[60vh] min-h-0 flex-col pb-2 pl-5">
      <TableList
        tables={tables}
        selected={selected}
        onSelect={(table) =>
          navigate({
            to: "/c/$connection",
            params: { connection },
            search: {
              tab: search.tab === "schema" ? "schema" : undefined,
              table,
            },
          })
        }
      />
    </div>
  );
}
