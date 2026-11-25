import { Link } from "@tanstack/react-router";
import type { ProjectOverview, ServiceView } from "../../data/types";
import { useOrg } from "../../shell/location";
import { LockMark, Panel, SectionHead, StateText } from "../../ui/kit";
import { serviceState } from "../../ui/status";
import { shortImage } from "../service/releases";
import { type Step, stages, step } from "./pipeline";

/** Services a grid shows before "N more" sends you to the project. */
const MAX_ROWS = 12;

/**
 * A project on the Projects page as a pipeline (ADR 0099): a row per
 * service, a column per environment in the order releases go through
 * them (previews first, production last), and between two columns what
 * lies there: the same image, settings that differ, a newer image with
 * Promote, or a promote that failed. Shown for projects with something
 * going on; the others are one row on the page.
 */
export function ProjectGrid({ overview }: { overview: ProjectOverview }) {
  const org = useOrg();
  const project = overview.project.name;
  const { previews, lanes } = stages(overview.environments);
  // Production's services first, in its order, then any only elsewhere.
  const names = [
    ...new Set(
      [...lanes]
        .reverse()
        .flatMap((e) => e.services.map((s) => s.service.name)),
    ),
  ];
  const find = (env: (typeof lanes)[number], name: string) =>
    env.services.find((s) => s.service.name === name);
  const shown = names.slice(0, MAX_ROWS);
  const id = `grid-${project}`;
  return (
    <section aria-labelledby={id}>
      <SectionHead
        id={id}
        right={
          lanes.length > 1 && (
            <Link
              to="/$org/$project/compare"
              params={{ org, project }}
              className="hover:text-fg"
            >
              Compare environments
            </Link>
          )
        }
      >
        <Link
          to="/$org/$project"
          params={{ org, project }}
          className="hover:underline"
        >
          {project}
        </Link>
      </SectionHead>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Service</th>
              {previews.length > 0 && <th>Previews</th>}
              {lanes.map((e, i) => (
                <LaneHead key={e.environment.name} env={e} first={i === 0} />
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((name) => (
              <tr key={name}>
                <td className="font-medium">{name}</td>
                {previews.length > 0 && (
                  <td className="font-mono text-xs text-mut">
                    {previewsOf(previews, name)}
                  </td>
                )}
                {lanes.map((e, i) => {
                  const here = find(e, name);
                  const before = lanes[i - 1];
                  return (
                    <LaneCells
                      key={e.environment.name}
                      project={project}
                      view={here}
                      // What lies between the column before and this one.
                      between={
                        before && {
                          from: before.environment.name,
                          to: e.environment.name,
                          step: step(before && find(before, name), here),
                        }
                      }
                    />
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      {names.length > shown.length && (
        <p className="mt-2 text-xs text-mut">
          <Link to="/$org/$project" params={{ org, project }}>
            {names.length - shown.length} more services in {project}
          </Link>
        </p>
      )}
    </section>
  );
}

/**
 * An environment's column heads: the step column before it (unnamed),
 * then its name with its lock and how many changes wait to apply.
 */
function LaneHead({
  env,
  first,
}: {
  env: ProjectOverview["environments"][number];
  first: boolean;
}) {
  return (
    <>
      {!first && (
        <th>
          <span className="sr-only">Between</span>
        </th>
      )}
      <th>
        <span className="inline-flex items-center gap-1.5">
          {env.environment.name}
          <LockMark lock={env.environment.lock} />
          {env.pending_changes > 0 && (
            <span className="font-normal text-warn">
              {env.pending_changes} staged
            </span>
          )}
        </span>
      </th>
    </>
  );
}

/** A service's previews by pull request: "pr212, pr88", or "none". */
function previewsOf(previews: ProjectOverview["environments"], name: string) {
  const prs = previews
    .filter((e) => e.services.some((s) => s.service.name === name))
    .map((e) => `pr${e.environment.preview?.number ?? "?"}`);
  if (prs.length === 0) return <span className="text-faint">none</span>;
  return prs.length > 2
    ? `${prs.slice(0, 2).join(", ")} +${prs.length - 2}`
    : prs.join(", ");
}

/**
 * A service's cells in one environment: what lies between it and the
 * column before (the step), then its state, release and image, linking
 * to the service there.
 */
function LaneCells({
  project,
  view,
  between,
}: {
  project: string;
  view: ServiceView | undefined;
  between?: { from: string; to: string; step: Step | undefined };
}) {
  const org = useOrg();
  const serving = view?.active_release;
  return (
    <>
      {between && (
        <td className="text-xs">
          <StepCell project={project} {...between} view={view} />
        </td>
      )}
      <td className="whitespace-nowrap">
        {view ? (
          <Link
            to="/$org/$project/$environment/$service"
            params={{
              org,
              project,
              environment: view.service.environment,
              service: view.service.name,
            }}
            className="inline-flex items-baseline gap-2 no-underline hover:underline"
          >
            <StateText state={serviceState(view)} className="text-xs" />
            {serving && (
              <span className="font-mono text-xs text-sec">
                r{serving.number} {shortImage(serving.image_ref)}
              </span>
            )}
          </Link>
        ) : (
          <span className="text-faint">none</span>
        )}
      </td>
    </>
  );
}

/**
 * What lies between two environments for one service, said briefly:
 * the earlier one's image failing in the later one links to that release, a newer image offers Promote (the
 * project's compare page with the promote panel open), settings that
 * differ are counted with the list in the title. A service's
 * environment strip says it the same way.
 */
export function StepCell({
  project,
  from,
  to,
  step: s,
  view,
}: {
  project: string;
  from: string;
  to: string;
  step: Step | undefined;
  view: ServiceView | undefined;
}) {
  const org = useOrg();
  if (!s) return null;
  const differ =
    s.diffs.length > 0
      ? `${s.diffs.length === 1 ? `${s.diffs[0]?.label} differs` : `${s.diffs.length} settings differ`}`
      : "";
  const title = s.diffs
    .map((d) => `${d.label}: ${d.from} in ${from}, ${d.to} in ${to}`)
    .join("\n");
  if (s.kind === "failed" && view) {
    return (
      <Link
        to="/$org/$project/$environment/$service/history/$number"
        params={{
          org,
          project,
          environment: view.service.environment,
          service: view.service.name,
          number: String(s.release.number),
        }}
        className="text-fail no-underline hover:underline"
        title={title || undefined}
      >
        failed in {to} as r{s.release.number}
        {differ && <span className="text-warn">, {differ}</span>}
      </Link>
    );
  }
  if (s.kind === "ahead") {
    return (
      <span className="inline-flex items-center gap-2 whitespace-nowrap">
        <Link
          to="/$org/$project/compare"
          params={{ org, project }}
          search={{ from, to, promote: true }}
          className="btn sm no-underline"
          title={`Promote ${from}'s images to ${to}`}
        >
          Promote →
        </Link>
        {differ && (
          <span className="text-warn" title={title}>
            {differ}
          </span>
        )}
      </span>
    );
  }
  const what = s.kind === "behind" ? "older image" : "same image";
  return (
    <span className="text-faint" title={title || undefined}>
      {what}
      {differ && <span className="text-warn">, {differ}</span>}
    </span>
  );
}
