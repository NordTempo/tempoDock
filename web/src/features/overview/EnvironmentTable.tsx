import { Link, useNavigate } from "@tanstack/react-router";
import { domainNeedsAttention } from "../../data/problems";
import { serviceRef } from "../../data/queries";
import type { ServiceView } from "../../data/types";
import { since } from "../../ui/format";
import { Status } from "../../ui/kit";
import { changeText, serviceStatus } from "../../ui/status";
import { formatSetting } from "../settings/settings";

/** One environment's services, one dense row each. Rows open the service. */
export function EnvironmentTable({ services }: { services: ServiceView[] }) {
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
          <th className="num">CPU limit</th>
          <th className="num">Memory limit</th>
          <th>Volume</th>
          <th>Staged</th>
          <th>Deployed</th>
        </tr>
      </thead>
      <tbody>
        {services.map((svc) => {
          const { service, active_release: release, domains } = svc;
          const params = serviceRef(service);
          const attention = domains.filter(domainNeedsAttention).length;
          return (
            <tr
              key={service.id}
              className="row"
              onClick={() =>
                navigate({ to: "/$project/$environment/$service", params })
              }
            >
              <td>
                <Link
                  to="/$project/$environment/$service"
                  params={params}
                  className="font-semibold"
                >
                  {service.name}
                </Link>
              </td>
              <td>
                <Status {...serviceStatus(svc)} />
              </td>
              <td className="font-mono text-xs">
                {release ? `r${release.number}` : "-"}
              </td>
              <td className="max-w-[280px] truncate font-mono text-xs text-dim">
                {release ? changeText(release) : service.image || "-"}
              </td>
              <td>
                {domains[0] ? (
                  <>
                    {domains[0].hostname}
                    {domains.length > 1 && (
                      <span className={attention ? "text-warn" : "text-dim"}>
                        {" "}
                        +{domains.length - 1}
                      </span>
                    )}
                  </>
                ) : (
                  <span className="text-dim">private</span>
                )}
              </td>
              <td className="num font-mono text-xs text-dim">
                {formatSetting("cpus", service.cpus)}
              </td>
              <td className="num font-mono text-xs text-dim">
                {formatSetting("memory_mb", service.memory_mb)}
              </td>
              <td className={service.stateful ? undefined : "text-dim"}>
                {service.stateful ? "yes" : "-"}
              </td>
              <td
                className={service.pending_changes ? "text-warn" : "text-dim"}
              >
                {service.pending_changes || "-"}
              </td>
              <td className="text-dim">
                {release ? since(release.created_at) : "never"}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
