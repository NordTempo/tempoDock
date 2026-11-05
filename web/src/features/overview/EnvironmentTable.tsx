import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { lazy, memo, Suspense, useState } from "react";
import { domainNeedsAttention } from "../../data/problems";
import {
  listenPort,
  queries,
  serviceRef,
  useCanDeploy,
  useIsAdmin,
  useIsOperator,
  useSession,
} from "../../data/queries";
import type { EnvironmentView, ServiceView } from "../../data/types";
import { useOrg } from "../../shell/location";
import { plural } from "../../ui/format";
import { Button, Panel, SectionHead, StateText } from "../../ui/kit";
import { kindLabel, serviceState } from "../../ui/status";
import { canCreateEnvironment } from "../create/access";
import { openCreate } from "../create/open";
import { MoveStrip } from "../move/MoveStrip";
import { previewsCli } from "../previews/data";
import {
  ClosePreview,
  PreviewLine,
  PullRequestLink,
} from "../previews/PreviewParts";
import { EnvServer } from "../servers/parts";
import { imageLabel } from "../service/releases";
import { CpuUsage, MemoryUsage } from "../service/Usage";
import { ArchivedServices } from "./Archived";
import { LockForm, LockStrip } from "./EnvironmentLock";

const MovePanel = lazy(() => import("../move/MovePanel"));

/**
 * One environment on the project overview: a row per service. A preview
 * environment (ADR 0056) also shows its pull request, status and
 * addresses, and can be closed.
 */
export function EnvironmentTable({ env }: { env: EnvironmentView }) {
  const org = useOrg();
  const { environment, services, pending_changes: staged } = env;
  const ref = { project: environment.project, environment: environment.name };
  const canDeploy = useCanDeploy(ref);
  // Cloning creates an environment: deploy on the whole project, which
  // closing a preview needs too.
  const canClone = canCreateEnvironment(useSession(), ref.project);
  const admin = useIsAdmin();
  const [locking, setLocking] = useState(false);
  const pr = environment.preview;
  const [closing, setClosing] = useState(false);
  const newService = () => openCreate({ kind: "service", env: ref });
  // Moving to another server (ADR 0053): operators open it from here;
  // anyone opens a running move's progress from its strip.
  const operator = useIsOperator();
  const [moveOpen, setMoveOpen] = useState(false);
  const links = "text-xs text-mut hover:text-fg";
  return (
    <section aria-labelledby={`env-${environment.id}`}>
      <SectionHead
        id={`env-${environment.id}`}
        aside={
          <>
            {pr && (
              <>
                <PullRequestLink pr={pr} /> ·{" "}
              </>
            )}
            on <EnvServer name={environment.server} /> ·{" "}
            {kindLabel[environment.kind]}
          </>
        }
        right={
          <>
            {staged > 0 && (
              <Link
                to="/$org/$project/settings/variables/$environment"
                params={{ org, ...ref }}
                className="text-warn hover:text-warn-text"
              >
                ▲ {plural(staged, "staged change")}
              </Link>
            )}
            <Link
              to="/$org/$project/settings/variables/$environment"
              params={{ org, ...ref }}
              className={links}
            >
              Variables
            </Link>
            <Link
              to="/$org/$project/settings/variables/$environment"
              params={{ org, ...ref }}
              hash="log-drains"
              className={links}
            >
              Log drains
            </Link>
            {canDeploy && (
              <button
                type="button"
                className={links}
                title={`dock service create -e ${ref.environment}`}
                onClick={newService}
              >
                New service
              </button>
            )}
            {canClone && (
              <button
                type="button"
                className={links}
                title={`dock environment clone ${ref.environment} <name>`}
                onClick={() =>
                  openCreate({
                    kind: "environment",
                    project: ref.project,
                    cloneFrom: ref.environment,
                  })
                }
              >
                Clone
              </button>
            )}
            {admin && !environment.lock && (
              <button
                type="button"
                className={links}
                title={`dock environment lock ${ref.environment}`}
                aria-expanded={locking}
                onClick={() => setLocking(!locking)}
              >
                Lock
              </button>
            )}
            {pr && canClone && (
              <button
                type="button"
                className="text-xs text-mut hover:text-fail"
                title={previewsCli(`close ${ref.environment}`, ref.project)}
                onClick={() => setClosing(true)}
              >
                Close preview
              </button>
            )}
            {operator && (
              <button
                type="button"
                className={moveOpen ? "text-xs text-fg" : links}
                title={`dock env move ${ref.environment} --to <server>`}
                aria-expanded={moveOpen}
                onClick={() => setMoveOpen(!moveOpen)}
              >
                Move
              </button>
            )}
          </>
        }
      >
        {environment.name}
      </SectionHead>
      {environment.lock ? (
        <LockStrip envRef={ref} lock={environment.lock} />
      ) : (
        locking && <LockForm envRef={ref} onDone={() => setLocking(false)} />
      )}
      {pr && <PreviewLine env={ref} />}
      {closing && <ClosePreview env={ref} onDone={() => setClosing(false)} />}
      {!moveOpen && (
        <MoveStrip
          env={ref}
          movingTo={environment.moving_to}
          onDetails={() => setMoveOpen(true)}
        />
      )}
      {moveOpen && (
        <Suspense fallback={null}>
          <MovePanel
            env={ref}
            server={environment.server}
            kind={environment.kind}
            operator={operator}
          />
        </Suspense>
      )}
      {services.length === 0 ? (
        <p className="flex flex-wrap items-center gap-x-2.5 gap-y-2 rounded-panel border border-dashed border-dash px-3.5 py-3 text-mut">
          No services yet.
          {canDeploy && (
            <Button small primary onClick={newService}>
              New service
            </Button>
          )}
          <span>
            {canDeploy ? "or deploy a directory with" : "Deploy one with"}{" "}
            <code className="font-mono text-fg">
              dock deploy -e {environment.name}
            </code>
          </span>
        </p>
      ) : (
        <Panel>
          {/* Fixed columns line up across the environments stacked here. */}
          <table className="table-fixed">
            <colgroup>
              <col className="w-[30%]" />
              <col className="w-[14%]" />
              <col className="w-[8%]" />
              <col className="w-[20%]" />
              <col className="w-[16%]" />
              <col className="w-[12%]" />
            </colgroup>
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
      <ArchivedServices env={env} />
    </section>
  );
}

const ServiceRow = memo(function ServiceRow({ view }: { view: ServiceView }) {
  const org = useOrg();
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
          to="/$org/$project/$environment/$service"
          params={{ org, ...ref }}
          className="font-medium hover:underline"
        >
          {service.name}
        </Link>
        <div
          className="truncate font-mono text-2xs text-faint"
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
              to="/$org/$project/$environment/$service/releases/$number"
              params={{ org, ...ref, number: String(failed.number) }}
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
        {service.staged === "archive" && (
          <div className="text-2xs text-mut">archive staged</div>
        )}
      </td>
      <td className="py-2.5 font-mono">
        {active ? `r${active.number}` : <span className="text-faint">·</span>}
        {deploying && <span className="text-prog"> → r{deploying.number}</span>}
      </td>
      <td className="truncate py-2.5 font-mono">
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
          <span className="text-faint">·</span>
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
          <span className="text-faint">·</span>
        )}
      </td>
    </tr>
  );
});
