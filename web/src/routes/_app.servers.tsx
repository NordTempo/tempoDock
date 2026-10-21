import { createFileRoute } from "@tanstack/react-router";
import { useProjectTree, useServers } from "../data/queries";
import { since } from "../ui/format";
import { Page, Scroll, TitleRow } from "../ui/kit";

export const Route = createFileRoute("/_app/servers")({
  component: Servers,
});

function Servers() {
  const servers = useServers();
  const tree = useProjectTree();
  const online = servers.filter((s) => s.status === "online").length;
  const offline = servers.length - online;
  // Environments by server, from every project's overview.
  const placed = (server: string) =>
    tree.flatMap((p) =>
      p.environments
        .filter((e) => e.environment.server === server)
        .map((e) => `${p.project.name}/${e.environment.name}`),
    );

  return (
    <Page gap="gap-[18px]">
      <TitleRow
        title="Servers"
        context={`${online} online${offline ? ` · ${offline} offline` : ""}`}
        actions={
          <span className="self-center font-mono text-2xs text-mut">
            dock server add
          </span>
        }
      />
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>Server</th>
              <th>Status</th>
              <th>Role</th>
              <th>Builds images</th>
              <th>Environments on it</th>
              <th>Control plane</th>
            </tr>
          </thead>
          <tbody>
            {servers.map((s) => {
              const envs = placed(s.name);
              const offline = s.status === "offline";
              return (
                <tr
                  key={s.id}
                  id={s.name}
                  className={offline ? "bg-[#140808]" : undefined}
                >
                  <td className="py-2.5 font-mono font-medium">{s.name}</td>
                  <td className="py-2.5">
                    {offline ? (
                      <>
                        <span className="text-fail">✕ offline</span>
                        {s.last_seen_at && (
                          <div className="text-xs text-mut">
                            last seen {since(s.last_seen_at)} ago
                          </div>
                        )}
                      </>
                    ) : (
                      <span className="text-ok">● online</span>
                    )}
                  </td>
                  <td className="py-2.5">{s.role}</td>
                  <td className={`py-2.5 ${s.builder ? "" : "text-mut"}`}>
                    {s.builder ? "yes" : "no"}
                  </td>
                  <td className="w-full py-2.5 font-mono whitespace-normal">
                    {envs.length ? (
                      envs.join(", ")
                    ) : (
                      <span className="font-sans text-mut">
                        none{offline ? ", safe to remove" : ""}
                      </span>
                    )}
                  </td>
                  <td className={`py-2.5 ${s.local ? "" : "text-mut"}`}>
                    {s.local ? "yes" : "no"}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Scroll>
    </Page>
  );
}
