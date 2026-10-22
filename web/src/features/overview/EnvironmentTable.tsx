import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { memo, useState } from "react";
import { domainNeedsAttention } from "../../data/problems";
import {
  queries,
  serviceRef,
  useCanDeploy,
  useSession,
} from "../../data/queries";
import type { EnvironmentView, ServiceView } from "../../data/types";
import { plural } from "../../ui/format";
import { Scroll, StateText } from "../../ui/kit";
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
  return (
    <section aria-labelledby={`env-${environment.id}`}>
      <h2 id={`env-${environment.id}`} className="text-md font-semibold">
        {environment.name}{" "}
        <span className="text-xs font-normal text-mut">
          <EnvServer name={environment.server} /> ·{" "}
          {kindLabel[environment.kind]}
        </span>{" "}
        {staged > 0 && (
          <Link
            to="/$project/$environment"
            params={ref}
            className="text-xs font-normal text-warn decoration-[#7a5c10]"
          >
            ▲ {plural(staged, "staged change")}
          </Link>
        )}
        <Link
          to="/$project/$environment"
          params={ref}
          className="ml-3 text-xs font-normal text-mut"
        >
          variables
        </Link>
        {canDeploy && !open && (
          <button
            type="button"
            className="ml-3 text-xs font-normal text-mut underline"
            onClick={() => setOpen("database")}
          >
            add database
          </button>
        )}
        {canClone && !open && (
          <button
            type="button"
            className="ml-3 text-xs font-normal text-mut underline"
            onClick={() => setOpen("clone")}
          >
            clone
          </button>
        )}
      </h2>
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
        <p className="py-2 text-mut">
          No services. Deploy one with{" "}
          <code className="font-mono text-white">
            dock deploy -e {environment.name}
          </code>
          .
        </p>
      ) : (
        <div className="mt-1">
          <Scroll>
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>State</th>
                  <th>Release</th>
                  <th>Image</th>
                  <th>Domain</th>
                  <th>CPU</th>
                  <th>Memory</th>
                  <th>Volume</th>
                </tr>
              </thead>
              <tbody>
                {services.map((s) => (
                  <ServiceRow key={s.service.id} view={s} />
                ))}
              </tbody>
            </table>
          </Scroll>
        </div>
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
    <tr className={deploying ? "bg-prog-bg" : undefined}>
      <td>
        <Link to="/$project/$environment/$service" params={ref}>
          {service.name}
        </Link>
      </td>
      <td>
        {failed ? (
          <>
            <Link
              to="/$project/$environment/$service/releases/$number"
              params={{ ...ref, number: String(failed.number) }}
              className="text-fail decoration-[#7a2e2e]"
            >
              ✕ r{failed.number} failed
            </Link>{" "}
            {active && (
              <span className="text-mut">r{active.number} serving</span>
            )}
          </>
        ) : (
          <StateText state={serviceState(view)} />
        )}
      </td>
      <td className="font-mono">
        {active ? `r${active.number}` : <span className="text-mut">none</span>}
        {deploying && <span className="text-prog"> → r{deploying.number}</span>}
      </td>
      <td className="font-mono text-mut">
        {oldImage || (!deploying && service.image) || ""}
        {deploying && newImage !== oldImage && (
          <span className="text-prog">
            {oldImage && " → "}
            {newImage}
          </span>
        )}
      </td>
      <td className="font-mono">
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
          <span className="font-sans text-mut">none</span>
        )}
      </td>
      <td className="font-mono">
        <CpuUsage service={service} />
      </td>
      <td className="font-mono">
        <MemoryUsage service={service} />
      </td>
      <td className="font-mono">
        {volumes.length > 0 ? (
          volumes.map((v) => (
            <span key={v.name} title={v.mount_path} className="mr-2">
              {v.name}
              {v.pending && <span className="text-warn"> staged</span>}
            </span>
          ))
        ) : (
          <span className="font-sans text-mut">none</span>
        )}
      </td>
    </tr>
  );
});
