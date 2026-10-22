import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Head, Scroll } from "../../../src/features/data/kit";
import { levelText } from "../../../src/features/data/policy";
import { useShortcuts } from "../../../src/shell/keys";
import { queries, useIsAdmin } from "../data/queries";

// The connections the caller may see, one dense line each.
export const Route = createFileRoute("/_app/")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.connections()),
  component: Connections,
});

function Connections() {
  const admin = useIsAdmin();
  const connections = useQuery(queries.connections());
  const navigate = useNavigate();
  useShortcuts(
    admin
      ? [
          {
            key: "n",
            label: "add connection",
            run: () => navigate({ to: "/connections/new" }),
          },
        ]
      : [],
  );
  const list = connections.data ?? [];

  return (
    <>
      <Head
        title="Connections"
        actions={
          admin && (
            <Link to="/connections/new" className="g-btn pri text-white!">
              Add connection
            </Link>
          )
        }
      >
        <span className="text-xs text-(--g-mut)">{list.length}</span>
      </Head>
      <main className="px-3 py-2">
        {list.length === 0 ? (
          <p className="px-2 py-3 text-(--g-mut)">
            {admin
              ? "No connections yet. Add one with a postgres:// URL."
              : "No connections you can see yet. An admin can add one or give you access."}
          </p>
        ) : (
          <Scroll>
            <table className="g-table min-w-[640px]">
              <thead>
                <tr>
                  <th>name</th>
                  <th>database</th>
                  <th>you</th>
                  {admin && <th className="text-right">pending</th>}
                </tr>
              </thead>
              <tbody>
                {list.map((c) => (
                  <tr key={c.name}>
                    <td className="w-[220px]">
                      <Link
                        to="/c/$connection"
                        params={{ connection: c.name }}
                        className="font-medium text-(--g-text)!"
                      >
                        {c.name}
                      </Link>
                      {c.production && (
                        <span className="ml-2 text-xs text-(--g-warn)">
                          production
                        </span>
                      )}
                    </td>
                    <td className="font-mono text-xs text-(--g-dim)">
                      {c.user}@{c.host}
                      {c.port === 5432 ? "" : `:${c.port}`}/{c.database}
                    </td>
                    <td className="text-(--g-dim)">
                      {admin ? "admin" : levelText[c.access.level]}
                      {c.access.sensitive === "visible" &&
                        ", sensitive visible"}
                    </td>
                    {admin && (
                      <td className="text-right">
                        {c.pending ? (
                          <Link
                            to="/c/$connection"
                            params={{ connection: c.name }}
                            search={{ tab: "changes" }}
                            className="text-(--g-warn)!"
                          >
                            {c.pending}
                          </Link>
                        ) : (
                          <span className="text-(--g-faint)">0</span>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </Scroll>
        )}
      </main>
    </>
  );
}
