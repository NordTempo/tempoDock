import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useArchiveService, useStageSetting } from "../../data/mutations";
import {
  usePendingChanges,
  useReleases,
  useService,
  useSession,
} from "../../data/queries";
import type { PendingChange, Service, SettingChange } from "../../data/types";
import { mb } from "../../ui/format";
import { Action, Button, ErrorText, Heading } from "../../ui/kit";
import { nextRelease } from "../service/releases";
import { ApplyBar } from "../variables/ApplyBar";
import {
  formatSetting,
  type SettingKey,
  settingChange,
  settingLabel,
} from "./settings";

const isSettingChange = (c: PendingChange): c is SettingChange =>
  c.kind === "setting";

/** Service settings with staged edits, a diff preview, and archive. */
export function SettingsPanel({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId);
  const releases = useReleases(serviceId);
  const changes = usePendingChanges(service.environmentId)
    .filter(isSettingChange)
    .filter((c) => c.serviceId === serviceId);
  const { settings, source, volume } = service;

  const field = (key: SettingKey) => (
    <EditableSetting
      service={service}
      settingKey={key}
      change={changes.find((c) => c.key === key)}
    />
  );

  return (
    <div className="px-5">
      <Heading>Settings</Heading>
      <dl className="grid max-w-[760px] grid-cols-[180px_1fr] gap-x-4 gap-y-1.5 pt-1 pb-2">
        <Item label="Source">
          {source.kind === "upload" && (
            <>
              upload · Dockerfile <Mono dim>{source.dockerfile}</Mono>, context{" "}
              <Mono dim>{source.context}</Mono>
            </>
          )}
          {source.kind === "image" && (
            <>
              image <Mono dim>{source.ref}</Mono>
            </>
          )}
          {source.kind === "git" && (
            <>
              git <Mono dim>{source.repo}</Mono> branch{" "}
              <Mono dim>{source.branch}</Mono>
            </>
          )}
        </Item>
        <Item label="Port">{field("port")}</Item>
        <Item label="Health check">
          <Mono>{settings.healthCheckPath ? "HTTP GET " : "TCP connect"}</Mono>
          {field("healthCheckPath")}
          <Mono>
            {" "}
            · every {settings.healthCheckIntervalS}s · timeout{" "}
            {settings.healthCheckTimeoutS}s
          </Mono>
        </Item>
        <Item label="Strategy">
          {settings.strategy === "rolling"
            ? "start new next to old, then cut over"
            : "stop old, then start new (the volume allows one writer)"}
        </Item>
        <Item label="Limits">
          <Mono>cpu </Mono>
          {field("cpuLimit")}
          <Mono> · memory </Mono>
          {field("memoryLimitMb")}
        </Item>
        <Item label="Volumes">
          {volume ? (
            <Mono>
              {volume.name} at {volume.mountPath} · {mb(volume.sizeMb)}
            </Mono>
          ) : (
            <span className="text-dim">none</span>
          )}
        </Item>
        <Item label="Server">
          {service.server.name}{" "}
          <span className="text-dim">
            from environment {service.environmentName}
          </span>
        </Item>
        <Item label="Restart policy">
          <Mono>{settings.restartPolicy}</Mono>
        </Item>
      </dl>

      {changes.length > 0 && (
        <>
          <DiffPreview
            service={service}
            changes={changes}
            release={nextRelease(service, releases)}
          />
          <ApplyBar
            environmentId={service.environmentId}
            changeIds={changes.map((c) => c.id)}
            note=""
            label="Apply"
          />
        </>
      )}

      <Archive service={service} />
    </div>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-dim">{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

function Mono({ dim, children }: { dim?: boolean; children: ReactNode }) {
  return (
    <span className={`font-mono text-xs ${dim ? "text-dim" : ""}`}>
      {children}
    </span>
  );
}

function EditableSetting({
  service,
  settingKey: key,
  change,
}: {
  service: Service;
  settingKey: SettingKey;
  change: SettingChange | undefined;
}) {
  const stage = useStageSetting();
  const [draft, setDraft] = useState<string | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const value = service.settings[key];

  if (draft !== null) {
    const save = () => {
      const next = settingChange(service.id, key, service.settings, draft);
      if (next instanceof Error) return setError(next);
      stage.mutate(
        { environmentId: service.environmentId, change: next },
        { onSuccess: () => setDraft(null) },
      );
    };
    return (
      <span className="inline-flex items-baseline gap-2">
        <input
          // biome-ignore lint/a11y/noAutofocus: opened by clicking the value
          autoFocus
          value={draft}
          aria-label={settingLabel[key]}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") save();
            if (e.key === "Escape") setDraft(null);
          }}
          className="w-24 border-b border-white bg-black font-mono text-xs outline-none"
        />
        <ErrorText error={error ?? stage.error} />
      </span>
    );
  }

  return (
    <button
      type="button"
      title={`Edit ${settingLabel[key]}`}
      onClick={() => {
        setError(null);
        setDraft(formatSetting(key, change ? change.after : value));
      }}
      className="font-mono text-xs hover:underline"
    >
      {change ? (
        <span className="text-warn">
          {formatSetting(key, change.before)} →{" "}
          {formatSetting(key, change.after)}
        </span>
      ) : (
        formatSetting(key, value)
      )}
    </button>
  );
}

function DiffPreview({
  service,
  changes,
  release,
}: {
  service: Service;
  changes: SettingChange[];
  /** e.g. "release r90 after r89 finishes" */
  release: string;
}) {
  const memory = changes.find((c) => c.key === "memoryLimitMb");
  const belowPeak = memory && Number(memory.after) < service.memory.peak24hMb;
  const facts = [
    memory && `peak in last 24h ${mb(service.memory.peak24hMb)}`,
    `creates ${release}`,
    service.settings.strategy === "rolling"
      ? "rolling restart, no downtime"
      : "stop then start, brief downtime",
  ].filter(Boolean);

  return (
    <div className="mt-1.5 max-w-[760px] border border-line2 px-2.5 py-2 font-mono text-xs leading-[1.6] whitespace-pre-wrap">
      <div className="text-dim">Pending change preview</div>
      {changes.map((c) => (
        <div key={c.id}>
          <div className="text-bad">
            - {settingLabel[c.key].padEnd(14)}
            {formatSetting(c.key, c.before)}
          </div>
          <div className="text-ok">
            + {settingLabel[c.key].padEnd(14)}
            {formatSetting(c.key, c.after)}
          </div>
        </div>
      ))}
      <div className="text-dim"> {facts.join(" · ")}</div>
      {belowPeak && (
        <div className="text-warn">
          {"  "}Below the 24h peak: the container may be killed for memory.
        </div>
      )}
    </div>
  );
}

function Archive({ service }: { service: Service }) {
  const session = useSession();
  const archive = useArchiveService();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const isAdmin = session?.role === "admin";

  return (
    <dl className="mt-2 grid max-w-[760px] grid-cols-[180px_1fr] gap-x-4 border-t border-line pt-3">
      <dt className="text-dim">Archive service</dt>
      <dd className="flex flex-wrap items-baseline gap-x-3">
        {confirming ? (
          <>
            <span>
              Stops {service.name} in {service.environmentName} now. Volumes are
              kept.
            </span>
            <Button
              className="border-bad! text-bad"
              disabled={archive.isPending}
              onClick={() =>
                archive.mutate(service.id, {
                  onSuccess: () =>
                    navigate({
                      to: "/projects/$projectId",
                      params: { projectId: service.projectId },
                    }),
                })
              }
            >
              Archive {service.name}
            </Button>
            <Action onClick={() => setConfirming(false)}>Cancel</Action>
            <ErrorText error={archive.error} />
          </>
        ) : (
          <>
            <button
              type="button"
              disabled={!isAdmin}
              onClick={() => setConfirming(true)}
              className="text-bad hover:underline disabled:text-faint disabled:no-underline"
            >
              Archive {service.name}
            </button>
            <span className="text-dim">
              requires admin. Stops it and keeps it 7 days.
            </span>
          </>
        )}
      </dd>
    </dl>
  );
}
