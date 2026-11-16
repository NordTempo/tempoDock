import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import type { ReactNode } from "react";
import {
  deployBlock,
  queries,
  serviceRef,
  useCanDeploy,
  useIsAdmin,
  useProjectOverview,
  useRevealedVariables,
  useVariables,
} from "../data/queries";
import type {
  EnvironmentView,
  ServiceView,
  Variable,
  Volume,
} from "../data/types";
import { PromotePanel } from "../features/promote/PromotePanel";
import {
  defaultPair,
  promoteCli,
  promotionPlan,
} from "../features/promote/plan";
import { EnvServer } from "../features/servers/parts";
import { imageLabel, shortImage } from "../features/service/releases";
import { useShortcuts } from "../shell/keys";
import { useOrg } from "../shell/location";
import { limits } from "../ui/format";
import { Act, Glyph, Page, PageBar, Panel } from "../ui/kit";
import { serviceState, toneText } from "../ui/status";

type CompareSearch = {
  from?: string;
  to?: string;
  all?: boolean;
  values?: boolean;
  /** The promote panel is open. */
  promote?: boolean;
};

// Two environments side by side, service by service: release, image,
// limits, variable names (values for admins who ask) and volumes. P
// promotes the source's images to the target.
export const Route = createFileRoute("/_app/$org/$project/compare")({
  validateSearch: (s: Record<string, unknown>): CompareSearch => ({
    from: typeof s.from === "string" ? s.from : undefined,
    to: typeof s.to === "string" ? s.to : undefined,
    all: s.all === true ? true : undefined,
    values: s.values === true ? true : undefined,
    promote: s.promote === true ? true : undefined,
  }),
  loader: async ({ context: { queryClient }, params }) => {
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    await Promise.all(
      overview.environments.map((e) =>
        queryClient.ensureQueryData(
          queries.variables(
            { project: params.project, environment: e.environment.name },
            null,
          ),
        ),
      ),
    );
  },
  component: Compare,
});

function Compare() {
  const org = useOrg();
  const { project } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { environments } = useProjectOverview(project);
  const fallback = defaultPair(environments);
  const fromName = search.from ?? fallback.from;
  const toName = search.to ?? fallback.to;
  const from = environments.find((e) => e.environment.name === fromName);
  const to = environments.find((e) => e.environment.name === toName);
  const isAdmin = useIsAdmin();
  const pair = from && to && from !== to ? { from, to } : undefined;
  // Release histories carry registry digests ("already runs this image").
  const digests = useQueries({
    queries: (pair ? [...pair.from.services, ...pair.to.services] : [])
      .filter((s) => s.active_release)
      .map((s) => queries.releases(serviceRef(s.service))),
    combine: (results) =>
      new Map(
        results.flatMap((r) =>
          (r.data ?? []).map((x) => [
            `${x.environment}/${x.service}/${x.number}`,
            x.image_digest,
          ]),
        ),
      ),
  });
  const plan = pair
    ? promotionPlan(pair.from, pair.to, (environment, service, number) =>
        digests.get(`${environment}/${service}/${number}`),
      )
    : [];
  const promotable = plan.filter((r) => !r.skip).length;
  const allowed = useCanDeploy({ project, environment: toName ?? "" });
  const blocked = deployBlock(to?.environment);
  const notReady = !pair
    ? "pick two different environments"
    : !allowed
      ? `promoting needs deploy permission on ${toName}`
      : (blocked ?? (promotable ? undefined : "nothing to promote"));
  const setPromote = (open: boolean) =>
    navigate({
      search: { ...search, promote: open || undefined },
      replace: true,
    });

  useShortcuts([
    {
      key: "p",
      label: "promote",
      disabled: !!notReady,
      run: () => setPromote(true),
    },
  ]);

  const pick = (
    label: string,
    value: string | undefined,
    key: "from" | "to",
  ) => (
    <select
      aria-label={label}
      value={value ?? ""}
      onChange={(e) =>
        navigate({ search: { ...search, [key]: e.target.value } })
      }
      // 28px, no taller than the title beside it, so the page bar keeps
      // every other page's height.
      className="field py-[4px] pr-7"
    >
      {environments.map((e) => (
        <option key={e.environment.id}>{e.environment.name}</option>
      ))}
    </select>
  );

  return (
    <>
      <PageBar
        parent={
          <Link
            to="/$org/$project"
            params={{ org, project }}
            className="hover:text-fg"
          >
            {project}
          </Link>
        }
        title="Compare"
        after={
          <span className="flex items-center gap-1.5 text-xs text-mut">
            {pick("From environment", fromName, "from")}
            <span className="text-faint">→</span>
            {pick("To environment", toName, "to")}
          </span>
        }
        actions={
          <span className="flex flex-wrap items-center gap-4 text-xs">
            {isAdmin && (
              <label className="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={!!search.values}
                  onChange={(e) =>
                    navigate({
                      search: {
                        ...search,
                        values: e.target.checked || undefined,
                      },
                    })
                  }
                />
                Compare values
              </label>
            )}
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                className="accent-fg"
                checked={!search.all}
                onChange={(e) =>
                  navigate({
                    search: {
                      ...search,
                      all: e.target.checked ? undefined : true,
                    },
                  })
                }
              />
              Only differences
            </label>
            <Act
              primary
              cli={promoteCli(fromName ?? "<from>", toName ?? "<to>")}
              disabled={!!notReady || search.promote}
              title={notReady}
              onClick={() => setPromote(true)}
            >
              Promote {promotable} {promotable === 1 ? "service" : "services"}{" "}
              <kbd>P</kbd>
            </Act>
          </span>
        }
      />
      <Page>
        {notReady && pair && (
          <p className="text-mut">Promote is off: {notReady}.</p>
        )}
        {pair && search.promote && (
          <PromotePanel
            key={`${fromName}:${toName}`}
            project={project}
            from={pair.from}
            to={pair.to}
            plan={plan}
            onClose={() => setPromote(false)}
          />
        )}
        {pair ? (
          <CompareTable
            project={project}
            from={pair.from}
            to={pair.to}
            all={!!search.all}
            values={!!search.values}
          />
        ) : (
          <p className="text-mut">Pick two different environments.</p>
        )}
      </Page>
    </>
  );
}

function CompareTable({
  project,
  from,
  to,
  all,
  values,
}: {
  project: string;
  from: EnvironmentView;
  to: EnvironmentView;
  all: boolean;
  values: boolean;
}) {
  const fromRef = { project, environment: from.environment.name };
  const toRef = { project, environment: to.environment.name };
  const fromVars = useVariables(fromRef, null);
  const toVars = useVariables(toRef, null);
  const fromValues = useRevealedVariables(fromRef, null, values).data;
  const toValues = useRevealedVariables(toRef, null, values).data;

  const names = [
    ...new Set([...from.services, ...to.services].map((s) => s.service.name)),
  ];
  const pairs = names.map((name) => ({
    name,
    a: from.services.find((s) => s.service.name === name),
    b: to.services.find((s) => s.service.name === name),
  }));
  const present = pairs.flatMap(({ a, b }) =>
    [a, b].flatMap((s) => (s ? [s] : [])),
  );
  const lists = useQueries({
    queries: present.map((s) => queries.volumes(serviceRef(s.service))),
    combine: (results) => results.map((r) => r.data ?? []),
  });
  const volumes = (s: ServiceView) => lists[present.indexOf(s)] ?? [];

  const rows = pairs.map((pair) => ({
    ...pair,
    diffs: differences(pair.a, pair.b, {
      a: fromValues ?? fromVars,
      b: toValues ?? toVars,
      byValue: !!(fromValues && toValues),
      volumes,
    }),
  }));
  const shown = all ? rows : rows.filter((r) => r.diffs.length > 0);

  return (
    <>
      <p className="text-sec">
        Promote moves only the <span className="text-warn">image</span>.
        Variables, volumes and settings stay as each environment has them.
      </p>
      <Panel>
        <table className="table-fixed">
          <thead>
            <tr>
              <th className="w-[120px]">Service</th>
              <th className="w-[24%] border-l border-line pl-4">
                {from.environment.name}{" "}
                <EnvServer environment={from.environment} />
              </th>
              <th className="w-[24%] border-l border-line pl-4">
                {to.environment.name} <EnvServer environment={to.environment} />
              </th>
              <th className="border-l border-line pl-4">Differences</th>
            </tr>
          </thead>
          <tbody>
            {shown.map(({ name, a, b, diffs }) => (
              <tr key={name}>
                <td className="py-2.5 font-medium">{name}</td>
                <td className="border-l border-line py-2.5 pl-4">
                  <Side
                    view={a}
                    project={project}
                    diff={diffs.some((d) => d.kind === "image")}
                  />
                </td>
                <td className="border-l border-line py-2.5 pl-4">
                  <Side view={b} project={project} diff={false} />
                </td>
                <td className="border-l border-line py-2.5 pl-4 whitespace-normal">
                  {diffs.length === 0 ? (
                    <span className="text-mut">same</span>
                  ) : (
                    <dl className="grid grid-cols-[72px_minmax(0,1fr)] gap-x-3 gap-y-1">
                      {diffs.map((d) => (
                        <div key={d.kind} className="contents">
                          <dt
                            className={
                              d.kind === "image" ? "text-warn" : "text-mut"
                            }
                          >
                            {d.kind}
                          </dt>
                          <dd className="min-w-0 break-words text-sec">
                            {d.detail}
                          </dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      {shown.length === 0 && (
        <p className="text-mut">
          <span className="text-ok">✓</span> No differences between{" "}
          {from.environment.name} and {to.environment.name}.
        </p>
      )}
    </>
  );
}

function Side({
  view,
  project,
  diff,
}: {
  view: ServiceView | undefined;
  project: string;
  diff: boolean;
}) {
  const org = useOrg();
  if (!view) return <span className="text-faint">not here</span>;
  const state = serviceState(view);
  const active = view.active_release;
  const image = (active ? imageLabel(active) : view.service.image) ?? "";
  const failed =
    !view.deploying && view.latest_release?.status === "failed"
      ? view.latest_release
      : undefined;
  return (
    <Link
      to="/$org/$project/$environment/$service"
      params={{ org, ...serviceRef(view.service), project }}
      className="block no-underline"
    >
      {failed ? (
        <span className="text-fail">✕ r{failed.number} failed </span>
      ) : (
        <span className={toneText[state.tone]}>
          {state.glyph && <Glyph char={state.glyph} />}{" "}
        </span>
      )}
      <span className="font-mono">
        {active ? `r${active.number}` : "none"}
        {view.deploying && ` → r${view.deploying.number}`}
      </span>
      <div
        title={image}
        className={`truncate font-mono text-xs ${diff ? "text-warn" : "text-mut"}`}
      >
        {shortImage(image)}
      </div>
    </Link>
  );
}

type Diff = { kind: string; detail?: ReactNode };

/**
 * What differs between one service in two environments, each as the
 * target's value now → the source's. Promote moves only the image.
 */
function differences(
  a: ServiceView | undefined,
  b: ServiceView | undefined,
  ctx: {
    a: Variable[];
    b: Variable[];
    byValue: boolean;
    volumes: (s: ServiceView) => Volume[];
  },
): Diff[] {
  if (!a || !b) {
    const where = (a ?? b)?.service.environment;
    return [{ kind: "only in", detail: where }];
  }
  const diffs: Diff[] = [];
  const ia = a.active_release?.image_id;
  const ib = b.active_release?.image_id;
  if (ia !== ib) {
    const label = (s: ServiceView) =>
      shortImage(
        (s.active_release ? imageLabel(s.active_release) : s.service.image) ??
          "",
      ) || "none";
    diffs.push({ kind: "image", detail: `${label(b)} → ${label(a)}` });
  }

  const vars = (list: Variable[], s: ServiceView) =>
    new Map(
      list
        .filter(
          (v) => v.scope === "environment" || v.service === s.service.name,
        )
        .map((v) => [v.key, v.value ?? ""]),
    );
  const va = vars(ctx.a, a);
  const vb = vars(ctx.b, b);
  const onlyA = [...va.keys()].filter((k) => !vb.has(k));
  const onlyB = [...vb.keys()].filter((k) => !va.has(k));
  const changed = ctx.byValue
    ? [...va.keys()].filter((k) => vb.has(k) && va.get(k) !== vb.get(k))
    : [];
  if (onlyA.length || onlyB.length || changed.length) {
    const envA = a.service.environment;
    const envB = b.service.environment;
    diffs.push({
      kind: "variables",
      detail: (
        <>
          {[
            onlyA.length > 0 && `${envA} only: ${onlyA.join(", ")}`,
            onlyB.length > 0 && `${envB} only: ${onlyB.join(", ")}`,
            changed.length > 0 && `different: ${changed.join(", ")}`,
          ]
            .filter(Boolean)
            .join(". ")}
        </>
      ),
    });
  }
  if (
    a.service.cpus !== b.service.cpus ||
    a.service.memory_mb !== b.service.memory_mb
  ) {
    diffs.push({
      kind: "limits",
      detail: `${limits(b.service)} → ${limits(a.service)}`,
    });
  }
  if (a.service.port !== b.service.port) {
    diffs.push({
      kind: "port",
      detail: `${b.service.port || 8080} → ${a.service.port || 8080}`,
    });
  }
  const mounts = (s: ServiceView) =>
    ctx
      .volumes(s)
      .map((v) => `${v.name}:${v.mount_path}`)
      .sort()
      .join(", ");
  // Volumes come from separate queries; equal-looking until both load.
  if (mounts(a) !== mounts(b)) {
    diffs.push({
      kind: "volumes",
      detail: `${mounts(b) || "none"} → ${mounts(a) || "none"}`,
    });
  }
  return diffs;
}
