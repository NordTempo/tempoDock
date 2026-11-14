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
  useSeesPlatform,
  useSession,
} from "../../data/queries";
import type {
  EnvironmentView,
  ReleaseSummary,
  ServiceView,
} from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, plural } from "../../ui/format";
import { Icon } from "../../ui/icons";
import { Act, Button, Menu, menuItem, Panel, StateText } from "../../ui/kit";
import { domainState, serviceState, toneText } from "../../ui/status";
import { sitesQuery } from "../analytics/loaders";
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
import { imageLabel, shortImage } from "../service/releases";
import { CpuUsage, MemoryUsage } from "../service/Usage";
import { ArchivedServices } from "./Archived";
import { LockForm, LockStrip } from "./EnvironmentLock";
import {
  duration,
  SleepForm,
  SleepState,
  sleepCli,
  useSleepEnvironment,
} from "./EnvironmentSleep";
import { hiddenFailures } from "./failed";
import { lockRequestDone, useLockRequested } from "./lockRequest";

const MovePanel = lazy(() => import("../move/MovePanel"));

/**
 * One environment on the project overview: a row per service. A preview
 * environment (ADR 0056) also shows its pull request, status and
 * addresses, and can be closed.
 */
export function EnvironmentTable({
  env,
  scoped = false,
}: {
  env: EnvironmentView;
  /** On the environment's own page, whose title already names it. */
  scoped?: boolean;
}) {
  const org = useOrg();
  const { environment, services, pending_changes: staged } = env;
  const ref = { project: environment.project, environment: environment.name };
  const canDeploy = useCanDeploy(ref);
  // Cloning creates an environment: deploy on the whole project, which
  // closing a preview needs too.
  const canClone = canCreateEnvironment(useSession(), ref.project);
  const admin = useIsAdmin();
  const [locking, setLocking] = useState(false);
  // The palette's "Lock <env>" opens the form here.
  if (useLockRequested(ref) && !locking) {
    lockRequestDone();
    setLocking(true);
  }
  const pr = environment.preview;
  const [closing, setClosing] = useState(false);
  const newService = () => openCreate({ kind: "service", env: ref });
  // Moving to another server (ADR 0053): operators open it from here;
  // anyone opens a running move's progress from its strip.
  const operator = useSeesPlatform();
  const [moveOpen, setMoveOpen] = useState(false);
  const hidden = hiddenFailures(services);
  const sleep = useSleepEnvironment();
  // Production never sleeps; an asleep one wakes from its heading.
  const canSleep =
    canDeploy &&
    environment.kind === "non_production" &&
    environment.sleep?.state !== "asleep" &&
    services.some((s) => s.service.status.state === "running");
  // When it sleeps on its own: admins of non-production environments;
  // previews take their base environment's preview setting (ADR 0070, 0077).
  const canSetSleep = admin && environment.kind === "non_production" && !pr;
  const [sleepForm, setSleepForm] = useState(false);
  const head = `env-head-${environment.id}`;
  const cli = (args: string) => `dock environment ${args} -p ${ref.project}`;
  return (
    <section id={`env-${environment.name}`} aria-labelledby={head}>
      {/* The name, what needs a look (staged changes, a server that's
          down, the preview's pull request) and the rest in one menu, so
          the service table outweighs its toolbar. */}
      <div className="mb-2.5 flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 id={head} className={scoped ? "sr-only" : "text-md font-semibold"}>
          {scoped ? (
            `${environment.name} services`
          ) : (
            <Link
              to="/$org/$project/$environment"
              params={{ org, ...ref }}
              className="hover:underline"
            >
              {environment.name}
            </Link>
          )}
        </h2>
        {pr && <PullRequestLink pr={pr} />}
        <SleepState environment={environment} />
        {environment.server_status !== "online" && (
          <span className="text-xs">
            <EnvServer environment={environment} />
          </span>
        )}
        {staged > 0 && (
          <Link
            to="/$org/$project/settings/variables/$environment"
            params={{ org, ...ref }}
            className="text-xs text-warn hover:text-warn-text"
          >
            ▲ {plural(staged, "change")} waiting to apply
          </Link>
        )}
        {admin && !environment.lock && (
          <Act
            small
            className="ml-auto"
            cli={cli(`lock ${ref.environment}`)}
            aria-expanded={locking}
            onClick={() => setLocking(!locking)}
          >
            <Icon name="lock" size={12} /> Lock
          </Act>
        )}
        {/* The same actions on the project and the environment page: Lock,
            and the rest in one named menu. */}
        <Menu
          label={`${environment.name}: more actions`}
          right
          className={admin && !environment.lock ? "" : "ml-auto"}
          button={
            // Named, so its contents can be guessed before opening it.
            <span className="btn sm">
              More <Icon name="chevronDown" size={12} />
            </span>
          }
        >
          {(close) => (
            <>
              {environment.server && (
                <span
                  role="presentation"
                  className="px-2.5 pt-1 pb-1.5 text-2xs text-mut"
                >
                  Runs on{" "}
                  <span className="font-mono text-sec">
                    {environment.server}
                  </span>
                </span>
              )}
              <Link
                to="/$org/$project/settings/variables/$environment"
                params={{ org, ...ref }}
                className={menuItem}
                role="menuitem"
                onClick={close}
              >
                Shared variables
              </Link>
              <Link
                to="/$org/$project/settings/log-drains"
                params={{ org, project: ref.project }}
                hash={`drains-${ref.environment}`}
                className={menuItem}
                role="menuitem"
                onClick={close}
              >
                Log drains
              </Link>
              {canDeploy && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={`dock service create <name> -e ${ref.environment} -p ${ref.project}`}
                  onClick={() => {
                    close();
                    newService();
                  }}
                >
                  New service in {ref.environment}
                </button>
              )}
              {canClone && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={cli(`clone ${ref.environment} <name>`)}
                  onClick={() => {
                    close();
                    openCreate({
                      kind: "environment",
                      project: ref.project,
                      cloneFrom: ref.environment,
                    });
                  }}
                >
                  Clone into a new environment
                </button>
              )}
              {canSetSleep && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={`dock env sleep-after ${ref.environment} <duration>|off -p ${ref.project}`}
                  aria-expanded={sleepForm}
                  onClick={() => {
                    close();
                    setSleepForm(true);
                  }}
                >
                  Sleep after…
                  {!!environment.sleep?.after_minutes && (
                    <span className="ml-1.5 font-mono text-2xs text-faint">
                      {duration(environment.sleep.after_minutes)}
                    </span>
                  )}
                </button>
              )}
              {canSleep && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={sleepCli("sleep", ref)}
                  onClick={() => {
                    close();
                    sleep.mutate(ref);
                  }}
                >
                  Put to sleep now
                </button>
              )}
              {pr && canClone && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={previewsCli(`close ${ref.environment}`, ref.project)}
                  onClick={() => {
                    close();
                    setClosing(true);
                  }}
                >
                  Close preview
                </button>
              )}
              {operator && (
                <button
                  type="button"
                  className={menuItem}
                  role="menuitem"
                  title={cli(`move ${ref.environment} --to <server>`)}
                  onClick={() => {
                    close();
                    setMoveOpen(!moveOpen);
                  }}
                >
                  {moveOpen
                    ? "Hide the move"
                    : `Move every service to another server`}
                </button>
              )}
            </>
          )}
        </Menu>
      </div>
      {environment.lock ? (
        <LockStrip envRef={ref} lock={environment.lock} />
      ) : (
        locking && <LockForm envRef={ref} onDone={() => setLocking(false)} />
      )}
      {sleepForm && (
        <SleepForm
          environment={environment}
          onDone={() => setSleepForm(false)}
        />
      )}
      {pr && <PreviewLine env={ref} />}
      {closing && <ClosePreview env={ref} onDone={() => setClosing(false)} />}
      {!moveOpen && (
        <MoveStrip
          env={ref}
          moving={environment.moving}
          movingTo={environment.moving_to}
          onDetails={() => setMoveOpen(true)}
        />
      )}
      {moveOpen && (
        <Suspense fallback={null}>
          <MovePanel
            env={ref}
            server={environment.server ?? ""}
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
              <col className="w-8" />
            </colgroup>
            <thead>
              <tr>
                <th>Service</th>
                <th>Status</th>
                <th>Release</th>
                <th>Domain</th>
                <th>CPU · memory</th>
                <th>Volume</th>
                <th>
                  <span className="sr-only">Open</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <ServiceRow
                  key={s.service.id}
                  view={s}
                  hidden={hidden.get(s.service.id)}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      <ArchivedServices env={env} />
    </section>
  );
}

/**
 * One service: its status, release, domains, usage and volumes. `hidden`
 * is a failed deploy a later rollback or restart hides (failed.ts),
 * shown under the status until a newer deploy succeeds.
 */
const ServiceRow = memo(function ServiceRow({
  view,
  hidden,
}: {
  view: ServiceView;
  hidden?: ReleaseSummary;
}) {
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
  // The analytics site counting this service's domains, if one does.
  const site = useQuery({
    ...sitesQuery(ref.project),
    select: (sites) =>
      sites.find(
        (x) =>
          !x.archived &&
          x.environment === ref.environment &&
          x.hostnames?.some((h) =>
            view.domains.some((d) => d.hostname === h.hostname),
          ),
      )?.name,
  }).data;
  const oldImage = active ? imageLabel(active) : "";
  const newImage = deploying ? imageLabel(deploying) : "";

  return (
    <tr className={`row-link ${deploying ? "bg-prog-bg" : ""}`}>
      <td className="py-2.5">
        <Link
          to="/$org/$project/$environment/$service"
          params={{ org, ...ref }}
          className="row-main font-medium hover:underline"
        >
          {service.name}
        </Link>
        <div
          className="truncate font-mono text-2xs text-faint"
          title={newImage || oldImage || service.image || undefined}
        >
          {shortImage(oldImage || (!deploying && service.image) || "")}
          {deploying && newImage !== oldImage && (
            <span className="text-prog">
              {oldImage && " → "}
              {shortImage(newImage)}
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
              to="/$org/$project/$environment/$service/history/$number"
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
        {hidden && (
          <div className="text-2xs">
            <Link
              to="/$org/$project/$environment/$service/history/$number"
              params={{ org, ...ref, number: String(hidden.number) }}
              className="text-fail hover:underline"
            >
              ✕ r{hidden.number} failed {ago(hidden.created_at)}
            </Link>
          </div>
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
            {rest.length > 0 && (
              <span className="text-mut"> +{rest.length}</span>
            )}
            {/* What a domain waits for, in words, not a bare mark. */}
            {primary.status !== "active" ? (
              <div
                className={`font-sans text-2xs ${toneText[domainState(primary).tone]}`}
              >
                {domainState(primary).label}
              </div>
            ) : (
              attention > 0 && (
                <div className="font-sans text-2xs text-warn">
                  {attention === 1
                    ? "1 domain needs attention"
                    : `${attention} domains need attention`}
                </div>
              )
            )}
          </>
        ) : (
          <span className="text-faint">·</span>
        )}
        {/* A labelled way to the site's visitors, under its domain. */}
        {site && (
          <div className="text-2xs">
            <Link
              to="/$org/$project/$environment/$service/requests/visitors"
              params={{ org, ...ref }}
              search={{ site }}
              className="text-mut underline decoration-dash underline-offset-3 hover:text-fg"
            >
              Visitors
            </Link>
          </div>
        )}
      </td>
      <td className="py-2.5 text-xs">
        {/* Wraps instead of running into the Volume column. */}
        <div className="pr-4 whitespace-normal">
          <CpuUsage service={service} />
        </div>
        <div className="pr-4 whitespace-normal">
          <MemoryUsage service={service} />
        </div>
      </td>
      <td className="py-2.5 font-mono text-xs">
        {volumes.length > 0 ? (
          volumes.map((v) => (
            <span key={v.name} title={v.mount_path} className="mr-2">
              {v.name}
              {v.pending && <span className="text-warn"> not applied</span>}
            </span>
          ))
        ) : (
          <span className="text-faint">·</span>
        )}
      </td>
      <td className="row-go" aria-hidden>
        <Icon name="chevronRight" size={14} />
      </td>
    </tr>
  );
});
