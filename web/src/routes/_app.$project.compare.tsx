import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import type { ReactNode } from "react";
import {
  queries,
  serviceRef,
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
import { imageLabel } from "../features/service/releases";
import { limits } from "../ui/format";
import { Glyph, Page, Scroll } from "../ui/kit";
import { serviceState, toneText } from "../ui/status";

type CompareSearch = {
  from?: string;
  to?: string;
  all?: boolean;
  values?: boolean;
};

// Two environments side by side, service by service: release, image,
// limits, variable names (values for admins who ask) and volumes.
export const Route = createFileRoute("/_app/$project/compare")({
  validateSearch: (s: Record<string, unknown>): CompareSearch => ({
    from: typeof s.from === "string" ? s.from : undefined,
    to: typeof s.to === "string" ? s.to : undefined,
    all: s.all === true ? true : undefined,
    values: s.values === true ? true : undefined,
  }),
  staticData: { crumb: "Compare" },
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

/** Staging-like first, production last, so "from → to" reads as a promotion. */
function defaults(envs: EnvironmentView[]) {
  const names = envs.map((e) => e.environment.name);
  const to = names.find((n) => n === "production") ?? names.at(-1);
  const from = names.find((n) => n !== to);
  return { from, to };
}

function Compare() {
  const { project } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { environments } = useProjectOverview(project);
  const fallback = defaults(environments);
  const fromName = search.from ?? fallback.from;
  const toName = search.to ?? fallback.to;
  const from = environments.find((e) => e.environment.name === fromName);
  const to = environments.find((e) => e.environment.name === toName);
  const isAdmin = useIsAdmin();

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
      className="field py-[5px]"
    >
      {environments.map((e) => (
        <option key={e.environment.id}>{e.environment.name}</option>
      ))}
    </select>
  );

  return (
    <Page gap="gap-[18px]">
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
        <h1 className="text-xl font-semibold">Compare</h1>
        <span className="flex items-center gap-1.5 text-mut">
          From {pick("From environment", fromName, "from")}
        </span>
        <span className="text-mut">→</span>
        {pick("To environment", toName, "to")}
        <span className="ml-auto flex items-center gap-4">
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
        </span>
      </div>
      <p className="text-mut">
        <span className="text-warn">Yellow</span> marks a difference. Variables
        compare by name{isAdmin ? " unless you compare values" : ""}.
      </p>
      {from && to && from !== to ? (
        <CompareTable
          project={project}
          from={from}
          to={to}
          all={!!search.all}
          values={!!search.values}
        />
      ) : (
        <p className="text-mut">Pick two different environments.</p>
      )}
    </Page>
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
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>Service</th>
              <th className="border-l border-line pl-4">
                {from.environment.name}{" "}
                <span className="font-mono">{from.environment.server}</span>
              </th>
              <th className="border-l border-line pl-4">
                {to.environment.name}{" "}
                <span className="font-mono">{to.environment.server}</span>
              </th>
              <th className="w-full border-l border-line pl-4">Differences</th>
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
                    diffs.map((d, i) => (
                      <span key={d.kind}>
                        {i > 0 && " · "}
                        <span className="text-warn">{d.kind}</span>
                        {d.detail && <>: {d.detail}</>}
                      </span>
                    ))
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Scroll>
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
  if (!view) return <span className="text-mut">not in this environment</span>;
  const state = serviceState(view);
  const active = view.active_release;
  const failed =
    !view.deploying && view.latest_release?.status === "failed"
      ? view.latest_release
      : undefined;
  return (
    <Link
      to="/$project/$environment/$service"
      params={{ ...serviceRef(view.service), project }}
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
      <div className={`font-mono ${diff ? "text-warn" : "text-mut"}`}>
        {active ? imageLabel(active) : view.service.image}
      </div>
    </Link>
  );
}

type Diff = { kind: string; detail?: ReactNode };

/** What differs between one service in two environments. */
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
  if (!a || !b) return [{ kind: "only in one environment" }];
  const diffs: Diff[] = [];
  const ia = a.active_release?.image_id;
  const ib = b.active_release?.image_id;
  if (ia !== ib) diffs.push({ kind: "image" });

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
            onlyA.length > 0 && `only ${envA} has ${onlyA.join(", ")}`,
            onlyB.length > 0 && `only ${envB} has ${onlyB.join(", ")}`,
            changed.length > 0 && `different ${changed.join(", ")}`,
          ]
            .filter(Boolean)
            .join("; ")}
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
      detail: `${limits(a.service)} vs ${limits(b.service)}`,
    });
  }
  if (a.service.port !== b.service.port) {
    diffs.push({
      kind: "port",
      detail: `${a.service.port || 8080} vs ${b.service.port || 8080}`,
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
      detail: `${mounts(a) || "none"} vs ${mounts(b) || "none"}`,
    });
  }
  return diffs;
}
