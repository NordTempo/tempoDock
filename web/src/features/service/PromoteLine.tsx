import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useProjectOverview } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { stages, step } from "../overview/pipeline";

/**
 * The line under a service's status sentence about its neighbours in the
 * pipeline (ADR 0099), only when there is something to do: the
 * environment before this one serves a newer image (Promote to here),
 * this one serves a newer image than the next (Promote there), or this
 * image failed in the next one. The same image, or an older one, says
 * nothing. A failed deploy here has its own panel (FailedDeploy).
 */
export function PromoteLine({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const lanes = stages(
    useProjectOverview(ref.project).environments,
  ).lanes.flatMap((e) => {
    const view = e.services.find((s) => s.service.name === ref.service);
    return view ? [{ env: e.environment.name, view }] : [];
  });
  const i = lanes.findIndex((l) => l.env === ref.environment);
  const here = lanes[i];
  if (!here) return null;
  const before = lanes[i - 1];
  const next = lanes[i + 1];
  const into = before && step(before.view, here.view);
  const out = next && step(here.view, next.view);

  const promote = (from: string, to: string) => (
    <Link
      to="/$org/$project/compare"
      params={{ org, project: ref.project }}
      search={{ from, to, promote: true }}
      className="text-sec underline decoration-faint underline-offset-2 hover:decoration-current"
    >
      Promote to {to} →
    </Link>
  );
  const said: ReactNode[] = [];
  if (before && into?.kind === "ahead") {
    const r = before.view.active_release?.number;
    said.push(
      <span key="into">
        {before.env} runs a newer image
        {r ? (
          <>
            , <span className="font-mono">r{r}</span>
          </>
        ) : null}
        . {promote(before.env, ref.environment)}
      </span>,
    );
  }
  if (next && out?.kind === "ahead") {
    const r = next.view.active_release?.number;
    said.push(
      <span key="out">
        {r ? (
          <>
            {next.env} runs an older image,{" "}
            <span className="font-mono">r{r}</span>.
          </>
        ) : (
          `Not in ${next.env} yet.`
        )}{" "}
        {promote(ref.environment, next.env)}
      </span>,
    );
  }
  if (next && out?.kind === "failed") {
    said.push(
      <span key="failed">
        This image{" "}
        <Link
          to="/$org/$project/$environment/$service/history/$number"
          params={{
            org,
            project: ref.project,
            environment: next.env,
            service: ref.service,
            number: String(out.release.number),
          }}
          className="text-fail underline decoration-faint underline-offset-2 hover:decoration-current"
        >
          failed in {next.env} as r{out.release.number}
        </Link>
        .
      </span>,
    );
  }
  if (said.length === 0) return null;
  // Tucked under the sentence, as its second line.
  return (
    <p className="-mt-2 flex flex-wrap gap-x-3 text-sm text-mut">{said}</p>
  );
}
