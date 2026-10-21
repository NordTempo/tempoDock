import { Link } from "@tanstack/react-router";
import { serviceRef } from "../../data/queries";
import type { ServiceView } from "../../data/types";
import { since } from "../../ui/format";
import { Actor, actorKind, Glyph } from "../../ui/kit";
import { stepPhrase } from "../../ui/status";

/** One line per deploy in flight, linking to its service. W opens the first. */
export function DeployBanner({
  service,
  watchKey,
}: {
  service: ServiceView;
  watchKey?: boolean;
}) {
  const release = service.deploying;
  if (!release) return null;
  const { name, environment } = service.service;
  const serving = service.service.active_release;
  return (
    <Link
      to="/$project/$environment/$service"
      params={serviceRef(service.service)}
      className="flex flex-wrap items-center gap-x-4 gap-y-1.5 bg-prog-bg px-3.5 py-3 no-underline"
    >
      <span className="text-prog">
        <Glyph char="◐" /> Deploying
      </span>
      <span className="text-md">
        <b className="font-semibold">{name}</b>{" "}
        <span className="text-mut">{environment}</span>{" "}
        <span className="font-mono">r{release.number}</span>,{" "}
        {stepPhrase[release.status]}.{" "}
        {serving && <span className="text-mut">r{serving} keeps serving.</span>}
      </span>
      <span className="text-mut">
        by{" "}
        <Actor name={release.created_by} kind={actorKind(release.created_by)} />{" "}
        · {since(release.created_at)}
      </span>
      {watchKey && (
        <span className="ml-auto">
          Watch <kbd>W</kbd>
        </span>
      )}
    </Link>
  );
}
