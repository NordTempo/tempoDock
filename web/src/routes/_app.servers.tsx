import { createFileRoute } from "@tanstack/react-router";
import { useServers } from "../data/queries";
import { TopBar } from "../shell/TopBar";
import { since } from "../ui/format";
import { Status } from "../ui/kit";
import { serverStatus } from "../ui/status";

export const Route = createFileRoute("/_app/servers")({
  component: Servers,
});

function Servers() {
  const servers = useServers();
  return (
    <>
      <TopBar>
        <span className="font-semibold">Servers</span>
      </TopBar>
      <div className="px-5 pt-3">
        <table className="tbl">
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Role</th>
              <th>Builder</th>
              <th>Control plane</th>
              <th className="num">Environments</th>
              <th className="w-full">Last seen</th>
            </tr>
          </thead>
          <tbody>
            {servers.map((s) => (
              <tr key={s.id} id={s.name}>
                <td className="font-semibold">{s.name}</td>
                <td>
                  <Status
                    tone={serverStatus(s).tone}
                    label={s.status}
                    colorLabel={s.status === "offline"}
                  />
                </td>
                <td>{s.role}</td>
                <td className="text-dim">{s.builder ? "yes" : "no"}</td>
                <td className="text-dim">{s.local ? "yes" : "no"}</td>
                <td className="num font-mono text-xs">{s.environments}</td>
                <td className="text-dim">
                  {s.last_seen_at ? since(s.last_seen_at) : "-"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="pt-3 text-dim">
          CPU, memory and disk usage are not collected yet.
        </p>
      </div>
    </>
  );
}
