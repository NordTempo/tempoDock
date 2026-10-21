import { Link, useNavigate } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useDeploy, useRestart, useRollback } from "../../data/mutations";
import { useReleases, useService } from "../../data/queries";
import { isTyping, useKeydown } from "../../shell/keys";
import { Crumbs, TopBar } from "../../shell/TopBar";
import { mb } from "../../ui/format";
import { Button, ErrorText, Status } from "../../ui/kit";
import { serviceStatus } from "../../ui/status";
import { rollbackTarget } from "./releases";

const tabs = [
  { label: "Releases", to: "/services/$serviceId" },
  { label: "Logs", to: "/services/$serviceId/logs" },
  { label: "Domains", to: "/services/$serviceId/domains" },
  { label: "Variables", to: "/services/$serviceId/variables" },
  { label: "Settings", to: "/services/$serviceId/settings" },
  { label: "Metrics", to: "/services/$serviceId/metrics" },
  { label: "Audit", to: "/services/$serviceId/audit" },
] as const;

/** Top bar, tabs (keys 1-7) and the at-a-glance meta row of a service. */
export function ServiceHeader({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId);
  const navigate = useNavigate();
  useKeydown((event) => {
    if (isTyping(event) || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    const tab = tabs[Number(event.key) - 1];
    if (!tab) return;
    event.preventDefault();
    navigate({ to: tab.to, params: { serviceId } });
  });

  return (
    <>
      <TopBar actions={<ServiceActions serviceId={serviceId} />}>
        <Crumbs>
          {[
            <Link
              key="p"
              to="/projects/$projectId"
              params={{ projectId: service.projectId }}
              className="hover:text-white"
            >
              {service.projectName}
            </Link>,
            service.environmentName,
            service.name,
          ]}
        </Crumbs>
        <Status {...serviceStatus(service.state)} />
      </TopBar>

      <nav className="sticky top-[41px] z-10 flex gap-5 border-b border-line bg-black px-5">
        {tabs.map((tab, i) => (
          <Link
            key={tab.to}
            to={tab.to}
            params={{ serviceId }}
            activeOptions={{ exact: true }}
            title={`${tab.label} (${i + 1})`}
            className="-mb-px border-b border-transparent py-[7px] text-dim hover:border-white hover:text-white"
            activeProps={{ className: "border-white! text-white!" }}
          >
            {tab.label}
            {tab.label === "Domains" && service.domains.attention > 0 && (
              <span className="text-warn"> {service.domains.attention}</span>
            )}
          </Link>
        ))}
      </nav>

      <div className="flex gap-7 border-b border-line px-5 py-3">
        <Meta label="Active">
          <b className="font-mono">
            {service.activeRelease ? `r${service.activeRelease.number}` : "-"}
          </b>
        </Meta>
        <Meta label="Server">{service.server.name}</Meta>
        <Meta label="Image" mono>
          {service.imageDigest ?? "-"}
        </Meta>
        <Meta label="CPU" mono>
          {service.cpuPercent}% of {service.settings.cpuLimit.toFixed(1)}
        </Meta>
        <Meta label="Memory" mono>
          {mb(service.memory.usedMb)} of {mb(service.settings.memoryLimitMb)}
        </Meta>
        <Meta label="Private" mono>
          {service.privateAddress}
        </Meta>
        {service.volume && (
          <Meta label="Volume" mono>
            {service.volume.name} {mb(service.volume.sizeMb)}
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
    <div>
      <span className="block text-2xs text-dim">{label}</span>
      <span className={mono ? "font-mono text-xs" : undefined}>{children}</span>
    </div>
  );
}

function ServiceActions({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId);
  const releases = useReleases(serviceId);
  const deploy = useDeploy();
  const restart = useRestart();
  const rollback = useRollback();
  const target = rollbackTarget(releases);
  const busy =
    service.state.state === "deploying"
      ? `r${service.state.release} is deploying`
      : undefined;
  const error = deploy.error ?? rollback.error ?? restart.error;

  return (
    <>
      <ErrorText error={error} />
      <Button
        disabled={restart.isPending}
        onClick={() => restart.mutate(serviceId)}
      >
        Restart
      </Button>
      <Button
        disabled={!target || !!busy || rollback.isPending}
        title={
          busy ??
          (target
            ? `Roll back to r${target.number}`
            : "No earlier release to roll back to")
        }
        onClick={() =>
          target && rollback.mutate({ serviceId, to: target.number })
        }
      >
        Rollback
      </Button>
      <Button
        primary
        disabled={!!busy || deploy.isPending}
        title={busy ?? "Rebuild and deploy the current source"}
        onClick={() => deploy.mutate(serviceId)}
      >
        Deploy
      </Button>
    </>
  );
}
