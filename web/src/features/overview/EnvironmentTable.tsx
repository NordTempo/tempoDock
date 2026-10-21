import { Link, useNavigate } from "@tanstack/react-router";
import type { Service } from "../../data/types";
import { mb, since } from "../../ui/format";
import { Bar, Status } from "../../ui/kit";
import { changeText, serviceStatus } from "../../ui/status";

/** One environment's services, one dense row each. Rows open the service. */
export function EnvironmentTable({ services }: { services: Service[] }) {
  const navigate = useNavigate();
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Service</th>
          <th>Status</th>
          <th>Release</th>
          <th>Source</th>
          <th>Domain</th>
          <th className="num">CPU</th>
          <th className="num">Memory</th>
          <th>Volume</th>
          <th>Updated</th>
        </tr>
      </thead>
      <tbody>
        {services.map((svc) => (
          <tr
            key={svc.id}
            className="row"
            onClick={() =>
              navigate({
                to: "/services/$serviceId",
                params: { serviceId: svc.id },
              })
            }
          >
            <td>
              <Link
                to="/services/$serviceId"
                params={{ serviceId: svc.id }}
                className="font-semibold"
              >
                {svc.name}
              </Link>
            </td>
            <td>
              <Status {...serviceStatus(svc.state)} />
            </td>
            <td className="font-mono text-xs">
              {svc.activeRelease ? `r${svc.activeRelease.number}` : "-"}
            </td>
            <td className="max-w-[280px] truncate font-mono text-xs text-dim">
              {svc.activeRelease ? changeText(svc.activeRelease.change) : "-"}
            </td>
            <td>
              {svc.domains.primary ? (
                <>
                  {svc.domains.primary}
                  {svc.domains.count > 1 && (
                    <span
                      className={
                        svc.domains.attention ? "text-warn" : "text-dim"
                      }
                    >
                      {" "}
                      +{svc.domains.count - 1}
                    </span>
                  )}
                </>
              ) : (
                <span className="text-dim">private</span>
              )}
            </td>
            <td className="num font-mono text-xs">{svc.cpuPercent}%</td>
            <td className="num font-mono text-xs">
              {mb(svc.memory.usedMb)} / {mb(svc.settings.memoryLimitMb)}
              <Bar fraction={svc.memory.usedMb / svc.settings.memoryLimitMb} />
            </td>
            <td className={svc.volume ? "font-mono text-xs" : "text-dim"}>
              {svc.volume ? `${svc.volume.name} ${mb(svc.volume.sizeMb)}` : "-"}
            </td>
            <td className="text-dim">{since(svc.updatedAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
