import { createFileRoute } from "@tanstack/react-router";
import { usePruneBuildCache } from "../data/mutations";
import { useServers } from "../data/queries";
import { TopBar } from "../shell/TopBar";
import { mb } from "../ui/format";
import { Action, Bar, ErrorText, Status } from "../ui/kit";
import { diskFraction, serverStatus } from "../ui/status";

export const Route = createFileRoute("/_app/servers")({
  component: Servers,
});

function Servers() {
  const servers = useServers();
  const prune = usePruneBuildCache();
  return (
    <>
      <TopBar>
        <span className="font-semibold">Servers</span>
        <ErrorText error={prune.error} />
      </TopBar>
      <div className="px-5 pt-3">
        <table className="tbl">
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Role</th>
              <th>Builder</th>
              <th>Address</th>
              <th className="num">CPU</th>
              <th className="num">Memory</th>
              <th className="num">Disk</th>
              <th className="num">Prunable</th>
              <th className="num">Environments</th>
              <th>Version</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {servers.map((s) => {
              const status = serverStatus(s);
              return (
                <tr key={s.id} id={s.id}>
                  <td className="font-semibold">{s.name}</td>
                  <td>
                    <Status
                      tone={status.tone}
                      label={s.status.state}
                      colorLabel={s.status.state === "offline"}
                    />
                  </td>
                  <td>{s.role}</td>
                  <td className="text-dim">{s.builder ? "yes" : "no"}</td>
                  <td className="font-mono text-xs">{s.address}</td>
                  <td className="num font-mono text-xs">{s.cpuPercent}%</td>
                  <td className="num font-mono text-xs">
                    {mb(s.memory.usedMb)} / {mb(s.memory.totalMb)}
                    <Bar fraction={s.memory.usedMb / s.memory.totalMb} />
                  </td>
                  <td className="num font-mono text-xs">
                    {s.disk.usedGb} / {s.disk.totalGb} GB
                    <Bar fraction={diskFraction(s)} />
                  </td>
                  <td className="num font-mono text-xs">
                    {s.disk.prunableGb} GB
                  </td>
                  <td className="num font-mono text-xs">
                    {s.environmentCount}
                  </td>
                  <td className="font-mono text-xs text-dim">{s.version}</td>
                  <td className="w-px">
                    {s.disk.prunableGb > 0 && (
                      <Action
                        disabled={prune.isPending}
                        onClick={() => prune.mutate(s.id)}
                      >
                        prune
                      </Action>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}
