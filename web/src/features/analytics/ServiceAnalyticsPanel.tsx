import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { queries, useCanDeploy } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Act, Confirm, CopyButton, ErrorText } from "../../ui/kit";
import { SettingsSection } from "../../ui/sections";
import { useCountVisits, useMoveSite, useStopCounting } from "./data";
import { sitesQuery } from "./loaders";
import { scriptTag } from "./ServiceSite";

/**
 * Settings' Analytics section (ADR 0104): whether Dock Analytics counts
 * the service's visits, the site they count for and the domains it
 * counts, the script its pages load, and moving the site with its history
 * to another service that replaced this one.
 */
export function ServiceAnalyticsPanel({
  serviceRef: ref,
}: {
  serviceRef: ServiceRef;
}) {
  const org = useOrg();
  const canDeploy = useCanDeploy(ref);
  const { data: sites } = useSuspenseQuery(sitesQuery(ref.project));
  const mine = sites.filter(
    (s) => s.environment === ref.environment && s.service === ref.service,
  );
  const site = mine.find((s) => !s.archived);
  const hostnames = site?.hostnames ?? [];
  const counted = mine.length > 0;
  const count = useCountVisits();
  const stop = useStopCounting();
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const why = canDeploy ? undefined : "needs deploy";
  return (
    <SettingsSection
      id="analytics"
      title="Analytics"
      actions={
        <>
          {site ? (
            <Act
              cli={`dock analytics disable ${flags}`}
              disabled={!canDeploy || stop.isPending}
              reason={why}
              onClick={() => stop.mutate(ref)}
            >
              Stop counting
            </Act>
          ) : (
            <Act
              primary
              cli={`dock analytics enable ${flags}`}
              disabled={!canDeploy || count.isPending}
              reason={why}
              onClick={() => count.mutate(ref)}
            >
              Count visits
            </Act>
          )}
          <ErrorText error={count.error ?? stop.error} />
        </>
      }
    >
      {site ? (
        <dl className="props">
          <dt>Site</dt>
          <dd>
            <Link
              to="/$org/analytics/$project/$site"
              params={{ org, project: ref.project, site: site.name }}
            >
              {site.name}
            </Link>
            <span className="text-mut">
              {" "}
              counts{" "}
              {hostnames.length > 0 ? (
                <span className="font-mono">
                  {hostnames.map((h) => h.hostname).join(", ")}
                </span>
              ) : (
                "its domains once it has one"
              )}
              ; redirects are left out
            </span>
          </dd>
          <dt>Script</dt>
          <dd className="flex flex-wrap items-center gap-2">
            <code className="font-mono text-fg">{scriptTag}</code>
            <CopyButton text={scriptTag} />
            <span className="text-mut">in each page's {"<head>"}</span>
          </dd>
          <dt>Move history</dt>
          <dd>
            <MoveSite serviceRef={ref} site={site.name} canDeploy={canDeploy} />
          </dd>
        </dl>
      ) : (
        <p className="text-mut">
          Not counted.{" "}
          {counted
            ? "Counting again brings back its last site with its history."
            : `Visits to every domain of ${ref.service} would count, without cookies; redirects are left out.`}
        </p>
      )}
    </SettingsSection>
  );
}

/**
 * Picks another service of the project that doesn't count visits yet and
 * hands it the site, after a confirm that says what happens.
 */
function MoveSite({
  serviceRef: ref,
  site,
  canDeploy,
}: {
  serviceRef: ServiceRef;
  site: string;
  canDeploy: boolean;
}) {
  const { data: targets } = useSuspenseQuery({
    ...queries.overview(ref.project),
    select: (o) =>
      o.environments.flatMap((e) =>
        (e.services ?? []).map((s) => ({
          environment: e.environment.name,
          service: s.service.name,
        })),
      ),
  });
  const { data: sites } = useSuspenseQuery(sitesQuery(ref.project));
  const counting = new Set(
    sites
      .filter((s) => !s.archived)
      .map((s) => `${s.environment}/${s.service}`),
  );
  const free = targets.filter(
    (t) => !counting.has(`${t.environment}/${t.service}`),
  );
  const [picked, setPicked] = useState("");
  const [confirming, setConfirming] = useState(false);
  const move = useMoveSite();
  const to = free.find((t) => `${t.environment}/${t.service}` === picked);
  const cli = `dock analytics move ${site} --to ${to?.service ?? "<service>"} -e ${to?.environment ?? ref.environment}`;
  if (free.length === 0) {
    return (
      <span className="text-mut">
        For a service that replaced {ref.service}: every other service of the
        project counts visits already.
      </span>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-2">
      <select
        aria-label="Service to move the site to"
        value={picked}
        disabled={!canDeploy}
        onChange={(e) => setPicked(e.target.value)}
      >
        <option value="">to another service…</option>
        {free.map((t) => (
          <option
            key={`${t.environment}/${t.service}`}
            value={`${t.environment}/${t.service}`}
          >
            {t.service}
            {t.environment === ref.environment ? "" : ` (${t.environment})`}
          </option>
        ))}
      </select>
      <Act
        small
        cli={cli}
        disabled={!to || !canDeploy}
        onClick={() => setConfirming(true)}
      >
        Move…
      </Act>
      {confirming && to && (
        <Confirm
          action={`Move ${site} to ${to.service}`}
          cli={cli}
          pending={move.isPending}
          error={move.error}
          onConfirm={() =>
            move.mutate(
              {
                project: ref.project,
                site,
                to: { environment: to.environment, service: to.service },
              },
              { onSuccess: () => setConfirming(false) },
            )
          }
          onCancel={() => setConfirming(false)}
        >
          {to.service}'s domains count for {site} from now on, with its history,
          settings, goals and funnels. {ref.service} stops counting.
        </Confirm>
      )}
    </span>
  );
}
