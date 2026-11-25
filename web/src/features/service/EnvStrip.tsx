import { Link } from "@tanstack/react-router";
import { useProjectOverview } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { StateText } from "../../ui/kit";
import { serviceState } from "../../ui/status";
import { StepCell } from "../overview/ProjectGrid";
import { stages, step } from "../overview/pipeline";
import { shortImage } from "./releases";

/**
 * The service in each of its project's environments, in the order a
 * release goes through them (ADR 0099): previews, then staging, then
 * production, with what lies between two (the same image, settings that
 * differ, Promote, a promote that failed). The page's own environment is
 * marked; the others open the same service there. Only when the service
 * is in more than one environment.
 */
export function EnvStrip({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const overview = useProjectOverview(ref.project);
  const { previews, lanes } = stages(overview.environments);
  const of = (e: (typeof lanes)[number]) =>
    e.services.find((s) => s.service.name === ref.service);
  const cells = lanes.flatMap((e) => {
    const view = of(e);
    return view ? [{ env: e.environment.name, view }] : [];
  });
  const prs = previews.filter((e) => of(e));
  if (cells.length + (prs.length > 0 ? 1 : 0) < 2) return null;
  return (
    <nav
      aria-label="Environments"
      className="flex flex-wrap items-stretch border-y border-line text-xs"
    >
      {prs.length > 0 && (
        <div className="flex flex-col justify-center border-r border-line py-1.5 pr-4">
          <span className="text-mut">
            {prs.length === 1 ? "preview" : `${prs.length} previews`}
          </span>
          <span className="font-mono text-sec">
            {prs
              .slice(0, 3)
              .map((e) => `pr${e.environment.preview?.number ?? "?"}`)
              .join(", ")}
          </span>
        </div>
      )}
      {cells.map(({ env, view }, i) => {
        const before = cells[i - 1];
        const here = env === ref.environment;
        const serving = view.active_release;
        return (
          <div key={env} className="flex items-stretch">
            {before && (
              <div className="flex items-center border-r border-line px-4 py-1.5">
                <StepCell
                  project={ref.project}
                  from={before.env}
                  to={env}
                  step={step(before.view, view)}
                  view={view}
                />
              </div>
            )}
            <Link
              to="/$org/$project/$environment/$service"
              params={{ org, ...ref, environment: env }}
              aria-current={here ? "page" : undefined}
              className={`flex flex-col justify-center border-r border-line px-4 py-1.5 no-underline ${here ? "bg-sel" : "hover:bg-hover"}`}
            >
              <span className={here ? "font-semibold text-fg" : "text-mut"}>
                {env}
              </span>
              <span className="inline-flex items-baseline gap-2">
                <StateText state={serviceState(view)} />
                {serving && (
                  <span className="font-mono text-sec">
                    r{serving.number} {shortImage(serving.image_ref)}
                  </span>
                )}
              </span>
            </Link>
          </div>
        );
      })}
    </nav>
  );
}
