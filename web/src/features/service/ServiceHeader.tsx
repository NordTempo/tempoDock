import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useDeploy, useRestart, useRollback } from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  useChanges,
  useEnvironment,
  useReleases,
  useService,
  useVolumes,
} from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { isTyping, useKeydown } from "../../shell/keys";
import { Crumbs, TopBar } from "../../shell/TopBar";
import { Button, ErrorText, Status } from "../../ui/kit";
import { serviceStatus, shortId } from "../../ui/status";
import { formatSetting } from "../settings/settings";
import { copiedConfig, rollbackTarget } from "./releases";

const tabs = [
  { label: "Releases", to: "/$project/$environment/$service" },
  { label: "Logs", to: "/$project/$environment/$service/logs" },
  { label: "Domains", to: "/$project/$environment/$service/domains" },
  { label: "Variables", to: "/$project/$environment/$service/variables" },
  { label: "Settings", to: "/$project/$environment/$service/settings" },
  { label: "Metrics", to: "/$project/$environment/$service/metrics" },
  { label: "Audit", to: "/$project/$environment/$service/audit" },
] as const;

/** Top bar, tabs (keys 1-7) and the at-a-glance meta row of a service. */
export function ServiceHeader({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const view = useService(ref);
  const { environment } = useEnvironment(ref);
  const volumes = useVolumes(ref);
  const changes = useChanges(ref);
  const navigate = useNavigate();
  useKeydown((event) => {
    if (isTyping(event) || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    const tab = tabs[Number(event.key) - 1];
    if (!tab) return;
    event.preventDefault();
    navigate({ to: tab.to, params: ref });
  });
  const { service, active_release: active, domains } = view;
  const mine = (c: { service?: string }) => c.service === ref.service;
  // Tabs count what needs a look: domains not active, staged changes.
  const badges: Partial<Record<(typeof tabs)[number]["label"], number>> = {
    Domains: domains.filter(domainNeedsAttention).length,
    Variables: changes.variables.filter((v) => !v.service || mine(v)).length,
    Settings:
      changes.settings.filter(mine).length +
      changes.volumes.filter(mine).length,
  };

  return (
    <>
      <TopBar actions={<ServiceActions serviceRef={ref} />}>
        <Crumbs>
          {[
            <Link
              key="p"
              to="/$project"
              params={{ project: ref.project }}
              className="hover:text-white"
            >
              {ref.project}
            </Link>,
            <Link
              key="e"
              to="/$project/$environment"
              params={{ project: ref.project, environment: ref.environment }}
              className="hover:text-white"
            >
              {ref.environment}
            </Link>,
            ref.service,
          ]}
        </Crumbs>
        <Status {...serviceStatus(view)} />
      </TopBar>

      <nav className="sticky top-[41px] z-10 flex gap-5 border-b border-line bg-black px-5">
        {tabs.map((tab, i) => (
          <Link
            key={tab.to}
            to={tab.to}
            params={ref}
            activeOptions={{ exact: true, includeSearch: false }}
            title={`${tab.label} (${i + 1})`}
            className="-mb-px border-b border-transparent py-[7px] text-dim hover:border-white hover:text-white"
            activeProps={{ className: "border-white! text-white!" }}
          >
            {tab.label}
            {!!badges[tab.label] && (
              <span className="text-warn"> {badges[tab.label]}</span>
            )}
          </Link>
        ))}
      </nav>

      <div className="flex gap-7 border-b border-line px-5 py-3">
        <Meta label="Active">
          <b className="font-mono">{active ? `r${active.number}` : "-"}</b>
        </Meta>
        <Meta label="Server">{environment.server}</Meta>
        <Meta label="Image" mono>
          {active
            ? `${active.image_ref} ${shortId(active.image_id)}`
            : service.image || "-"}
        </Meta>
        <Meta label="Limits" mono>
          {formatSetting("cpus", service.cpus)} cpu ·{" "}
          {formatSetting("memory_mb", service.memory_mb)}
        </Meta>
        <Meta label="Private" mono>
          {service.name}:{service.port || 8080}
        </Meta>
        {volumes.length > 0 && (
          <Meta label="Volumes" mono>
            {volumes.map((v) => `${v.name} ${v.mount_path}`).join(", ")}
          </Meta>
        )}
      </div>
    </>
  );
}

function Meta({
  label,
  mono,
  children,
}: {
  label: string;
  mono?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="min-w-0">
      <span className="block text-2xs text-dim">{label}</span>
      <span className={`block truncate ${mono ? "font-mono text-xs" : ""}`}>
        {children}
      </span>
    </div>
  );
}

function ServiceActions({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const deploy = useDeploy();
  const restart = useRestart();
  const rollback = useRollback();
  const target = rollbackTarget(releases);
  const busy = deploying ? `r${deploying.number} is deploying` : undefined;
  const error = deploy.error ?? rollback.error ?? restart.error;
  const [confirming, setConfirming] = useState(false);

  if (confirming && target) {
    return (
      <>
        <ErrorText error={rollback.error} />
        <span className="text-dim">
          Roll back to r{target.number}:{" "}
          <span className="font-mono text-xs text-white">
            {copiedConfig(target)}
          </span>
        </span>
        <Button
          primary
          disabled={!!busy || rollback.isPending}
          title={busy}
          onClick={() =>
            rollback.mutate(
              { ref, to: target.number },
              { onSuccess: () => setConfirming(false) },
            )
          }
        >
          Roll back
        </Button>
        <Button onClick={() => setConfirming(false)}>Cancel</Button>
      </>
    );
  }

  return (
    <>
      <ErrorText error={error} />
      <Button
        disabled={!service.active_release || !!busy || restart.isPending}
        title={
          busy ??
          (service.active_release
            ? "Start a new release identical to the active one"
            : "Nothing to restart")
        }
        onClick={() => restart.mutate(ref)}
      >
        Restart
      </Button>
      <Button
        disabled={!target || !!busy || rollback.isPending}
        title={
          busy ??
          (target
            ? `Roll back to r${target.number}: ${copiedConfig(target)}`
            : "No earlier release to roll back to")
        }
        onClick={() => setConfirming(true)}
      >
        Rollback
      </Button>
      <Button
        primary
        disabled={!service.image || !!busy || deploy.isPending}
        title={
          busy ??
          (service.image
            ? `Deploy ${service.image}`
            : "Built from source: deploy with `dock deploy`")
        }
        onClick={() => deploy.mutate(ref)}
      >
        Deploy
      </Button>
    </>
  );
}
