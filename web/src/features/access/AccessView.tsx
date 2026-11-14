import { useSuspenseQuery } from "@tanstack/react-query";
import { accessQueries, useRoles } from "./data";

// Effective access (ADR 0080): what a person can do, worked out from
// their groups and one-off grants. Rows are the area roles, columns the
// environments they see; a cell is the role's level there, and hovering
// it says which grants give it. Org-wide roles (members, org settings,
// usage) are one line above.

/** At most this many environments side by side; the rest scroll. */
const maxColumns = 40;

export function AccessView({ who }: { who: string }) {
  const acc = useSuspenseQuery(accessQueries.access(who)).data;
  const all = acc.environments ?? [];
  const groups = acc.groups ?? [];
  const org = acc.org ?? [];
  const roles = useRoles();
  const name = (key: string) => roles.find((r) => r.key === key)?.name ?? key;
  const envs = all.slice(0, maxColumns);
  const keys = roles
    .map((r) => r.key)
    .filter((k) => envs.some((e) => (e.roles ?? []).some((r) => r.role === k)));
  return (
    <div className="flex flex-col gap-3">
      <p>
        <span className="text-mut">Groups</span>{" "}
        {groups.length ? groups.join(", ") : "none"}
        {org.length > 0 && (
          <>
            {" · "}
            <span className="text-mut">On the organization</span>{" "}
            {org.map((r) => `${name(r.role)} ${r.level}`).join(", ")}
          </>
        )}
      </p>
      {envs.length === 0 ? (
        <p className="text-faint">No access to any environment.</p>
      ) : (
        <div className="overflow-x-auto">
          <table>
            <thead>
              <tr>
                <th />
                {envs.map((e) => (
                  <th
                    key={`${e.project}/${e.environment}`}
                    className="font-mono"
                  >
                    {e.project}
                    <br />
                    {e.environment}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k}>
                  <td className="whitespace-nowrap">{name(k)}</td>
                  {envs.map((e) => {
                    const r = (e.roles ?? []).find((x) => x.role === k);
                    return (
                      <td
                        key={`${e.project}/${e.environment}`}
                        className={r ? "" : "text-faint"}
                        title={r?.from?.join("\n")}
                      >
                        {r?.level ?? "·"}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {all.length > maxColumns && (
        <p className="text-faint">
          The first {maxColumns} environments; the CLI shows all:{" "}
          <code>dock can {who}</code>
        </p>
      )}
    </div>
  );
}
