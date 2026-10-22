import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { memo, useState } from "react";
import { domainNeedsAttention } from "../../data/problems";
import {
  listenPort,
  queries,
  serviceRef,
  useCanDeploy,
  useSession,
} from "../../data/queries";
import type { EnvironmentView, ServiceView } from "../../data/types";
import { plural } from "../../ui/format";
import { Panel, SectionHead, StateText } from "../../ui/kit";
import { kindLabel, serviceState } from "../../ui/status";
import { EnvServer } from "../servers/parts";
import { imageLabel } from "../service/releases";
import { CpuUsage, MemoryUsage } from "../service/Usage";
import { AddDatabase } from "../templates/AddDatabase";
import { CloneEnvironment } from "./CloneEnvironment";

/** One environment on the project overview: a row per service. */
export function EnvironmentTable({ env }: { env: EnvironmentView }) {
  const { environment, services, pending_changes: staged } = env;
  const ref = { project: environment.project, environment: environment.name };
  const canDeploy = useCanDeploy(ref);
  // Cloning creates an environment: deploy on the whole project.
  const limitedToEnv = !!useSession()?.environment;
  const canClone = canDeploy && !limitedToEnv;
  const [open, setOpen] = useState<"database" | "clone" | null>(null);
  const links = "text-xs text-mut hover:text-fg";
  return (
    <section aria-labelledby={`env-${environment.id}`}>
      <SectionHead
        id={`env-${environment.id}`}
        aside={
          <>
            on <EnvServer name={environment.server} /> ·{" "}
            {kindLabel[environment.kind]}
          </>
        }
        right={
          <>
            {staged > 0 && (
              <Link
                to="/$project/$environment"
                params={ref}
                className="text-warn hover:text-warn-text"
              >
                ▲ {plural(staged, "staged change")}
              </Link>
            )}
            <Link to="/$project/$environment" params={ref} className={links}>
              Variables
            </Link>
            {canDeploy && !open && (
              <button
                type="button"
                className={links}
                onClick={() => setOpen("database")}
              >
                Add database
              </button>
            )}
            {canClone && !open && (
              <button
                type="button"
                className={links}
                onClick={() => setOpen("clone")}
              >
                Clone
              </button>
            )}
          </>
        }
      >
        {environment.name}
      </SectionHead>
      {open === "database" && (
        <AddDatabase env={ref} onClose={() => setOpen(null)} />
      )}
      {open === "clone" && (
        <CloneEnvironment
          env={ref}
          kind={environment.kind}
          onClose={() => setOpen(null)}
        />
      )}
      {services.length === 0 ? (
        <p className="rounded-panel border border-dashed border-dash px-3.5 py-3 text-mut">
          No services. Deploy one with{" "}
          <code className="font-mono text-fg">
            dock deploy -e {environment.name}
          </code>
          .
        </p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th>Status</th>
                <th>Release</th>
                <th>Domain</th>
                <th>CPU · memory</th>
                <th>Volume</th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <ServiceRow key={s.service.id} view={s} />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
    </section>
  );
}

const ServiceRow = memo(function ServiceRow({ view }: { view: ServiceView }) {
  const {
    service,
    deploying,
    active_release: active,
    latest_release: latest,
  } = view;
  const ref = serviceRef(service);
  const volumes = useQuery(queries.volumes(ref)).data ?? [];
  const failed = !deploying && latest?.status === "failed" ? latest : undefined;
  const [primary, ...rest] = [...view.domains].sort(
    (a, b) => Number(a.generated) - Number(b.generated),
  );
  const attention = rest.filter(domainNeedsAttention).length;
  const oldImage = active ? imageLabel(active) : "";
  const newImage = deploying ? imageLabel(deploying) : "";

  return (
    <tr className={deploying ? "bg-prog-bg" : "hover:bg-hover"}>
      <td className="py-2.5">
        <Link
          to="/$project/$environment/$service"
          params={ref}
          className="font-medium hover:underline"
        >
          {service.name}
        </Link>
        <div
          className="max-w-[40ch] truncate font-mono text-2xs text-faint"
          title={newImage || oldImage || service.image || undefined}
        >
          {oldImage || (!deploying && service.image) || ""}
          {deploying && newImage !== oldImage && (
            <span className="text-prog">
              {oldImage && " → "}
              {newImage}
            </span>
          )}
          {(oldImage || service.image || newImage) && " · "}:
          {listenPort(service)}
        </div>
      </td>
      <td className="py-2.5">
        {failed ? (
          <>
            <Link
              to="/$project/$environment/$service/releases/$number"
              params={{ ...ref, number: String(failed.number) }}
              className="text-fail hover:underline"
            >
              ✕ r{failed.number} failed
            </Link>
            {active && (
              <div className="text-2xs text-faint">
                r{active.number} serving
              </div>
            )}
          </>
        ) : (
          <StateText state={serviceState(view)} />
        )}
      </td>
      <td className="py-2.5 font-mono">
        {active ? `r${active.number}` : <span className="text-mut">none</span>}
        {deploying && <span className="text-prog"> → r{deploying.number}</span>}
      </td>
      <td className="py-2.5 font-mono">
        {primary ? (
          <>
            {primary.hostname}
            {primary.status !== "active" && (
              <span className="text-warn"> ▲</span>
            )}
            {rest.length > 0 && (
              <span className={attention ? "text-warn" : "text-mut"}>
                {" "}
                +{rest.length}
                {attention > 0 && " ▲"}
              </span>
            )}
          </>
        ) : (
          <span className="font-sans text-faint">none</span>
        )}
      </td>
      <td className="py-2.5 text-xs">
        <div>
          <CpuUsage service={service} />
        </div>
        <div>
          <MemoryUsage service={service} />
        </div>
      </td>
      <td className="py-2.5 font-mono text-xs">
        {volumes.length > 0 ? (
          volumes.map((v) => (
            <span key={v.name} title={v.mount_path} className="mr-2">
              {v.name}
              {v.pending && <span className="text-warn"> staged</span>}
            </span>
          ))
        ) : (
          <span className="font-sans text-faint">none</span>
        )}
      </td>
    </tr>
  );
});
