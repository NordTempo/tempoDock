import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useArchiveService, useStageSettings } from "../../data/mutations";
import {
  envPath,
  useChanges,
  useEnvironment,
  useIsAdmin,
  useService,
  useVolumes,
} from "../../data/queries";
import type { ServiceRef, ServiceView, SettingChange } from "../../data/types";
import { Action, Button, ErrorText, Heading } from "../../ui/kit";
import { ApplyBar } from "../variables/ApplyBar";
import {
  buildOnly,
  editText,
  formatSetting,
  isSettingKey,
  parseSetting,
  type SettingKey,
  settingLabel,
} from "./settings";

type KeyedChange = SettingChange & { key: SettingKey };

/** Service settings with staged edits, a diff preview, and archive. */
export function SettingsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const view = useService(ref);
  const { environment } = useEnvironment(ref);
  const volumes = useVolumes(ref);
  const pending = useChanges(ref);
  const changes = pending.settings.flatMap((c) =>
    c.service === ref.service && isSettingKey(c.field)
      ? [{ ...c, key: c.field }]
      : [],
  );
  const { service } = view;

  const field = (key: SettingKey) => (
    <EditableSetting
      serviceRef={ref}
      settingKey={key}
      value={service[key]}
      change={changes.find((c) => c.key === key)}
    />
  );

  return (
    <div className="px-5">
      <Heading>Settings</Heading>
      <dl className="grid max-w-[760px] grid-cols-[180px_1fr] gap-x-4 gap-y-1.5 pt-1 pb-2">
        <Item label="Image">{field("image")}</Item>
        <Item label="Port">
          {field("port")}
          <Mono dim>
            {" "}
            · private {service.name}:{service.port || 8080}
          </Mono>
        </Item>
        <Item label="Start command">{field("start_command")}</Item>
        <Item label="Health check">
          {field("health_check_path")}
          <Mono dim> · timeout </Mono>
          {field("health_check_timeout_seconds")}
        </Item>
        <Item label="Strategy">
          {service.stateful
            ? "stop old, then start new (volumes allow one writer)"
            : "start new next to old, then cut over"}
        </Item>
        <Item label="Limits">
          <Mono dim>cpu </Mono>
          {field("cpus")}
          <Mono dim> · memory </Mono>
          {field("memory_mb")}
        </Item>
        <Item label="Build">
          {field("dockerfile_path")}
          <Mono dim> · </Mono>
          {field("build_command")}
        </Item>
        <Item label="Volumes">
          {volumes.length === 0 ? (
            <span className="text-dim">none</span>
          ) : (
            volumes.map((v) => (
              <Mono key={v.name}>
                {v.name} at {v.mount_path}
                {v.pending && <span className="text-warn"> staged</span>}{" "}
              </Mono>
            ))
          )}
        </Item>
        <Item label="Server">
          {environment.server}{" "}
          <span className="text-dim">from environment {environment.name}</span>
        </Item>
      </dl>

      {changes.length > 0 && <DiffPreview view={view} changes={changes} />}
      <ApplyBar env={envPath(ref)} />

      <Archive serviceRef={ref} />
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

/** A setting value; click to edit. Staged values show old → new and undo. */
function EditableSetting({
  serviceRef: ref,
  settingKey: key,
  value,
  change,
}: {
  serviceRef: ServiceRef;
  settingKey: SettingKey;
  value: string | number;
  change: KeyedChange | undefined;
}) {
  const stage = useStageSettings();
  const [draft, setDraft] = useState<string | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const submit = (raw: string, onSuccess?: () => void) => {
    const patch = parseSetting(key, raw);
    if (patch instanceof Error) return setError(patch);
    setError(null);
    stage.mutate({ ref, patch }, { onSuccess });
  };

  if (draft !== null) {
    return (
      <span className="inline-flex items-baseline gap-2">
        <input
          // biome-ignore lint/a11y/noAutofocus: opened by clicking the value
          autoFocus
          value={draft}
          aria-label={settingLabel[key]}
          placeholder={formatSetting(key, "")}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit(draft, () => setDraft(null));
            if (e.key === "Escape") setDraft(null);
          }}
          className="w-48 border-b border-white bg-black font-mono text-xs outline-none placeholder:text-faint"
        />
        <ErrorText error={error ?? stage.error} />
      </span>
    );
  }

  return (
    <span className="inline-flex items-baseline gap-2">
      <button
        type="button"
        title={`Edit ${settingLabel[key]}`}
        onClick={() => {
          setError(null);
          setDraft(editText(key, change ? change.new : value));
        }}
        className="font-mono text-xs hover:underline"
      >
        {change ? (
          <span className="text-warn">
            {formatSetting(key, change.old)} → {formatSetting(key, change.new)}
          </span>
        ) : (
          formatSetting(key, value)
        )}
      </button>
      {change && (
        <Action
          className="text-xs"
          disabled={stage.isPending}
          onClick={() => submit(change.old)}
        >
          undo
        </Action>
      )}
      <ErrorText error={error ?? stage.error} />
    </span>
  );
}

function DiffPreview({
  view,
  changes,
}: {
  view: ServiceView;
  changes: KeyedChange[];
}) {
  const { service, deploying } = view;
  const releases = changes.some((c) => !buildOnly(c.key));
  const facts = [
    !service.active_release
      ? "takes effect on the first deploy"
      : releases
        ? `creates a release${deploying ? ` after r${deploying.number} finishes` : ""}`
        : "takes effect on the next build",
    service.stateful
      ? "stop then start, brief downtime"
      : "rolling restart, no downtime",
  ];

  return (
    <div className="mt-1.5 max-w-[760px] border border-line2 px-2.5 py-2 font-mono text-xs leading-[1.6] whitespace-pre-wrap">
      <div className="text-dim">Pending change preview</div>
      {changes.map((c) => (
        <div key={c.key}>
          <div className="text-bad">
            - {settingLabel[c.key].padEnd(22)}
            {formatSetting(c.key, c.old)}
          </div>
          <div className="text-ok">
            + {settingLabel[c.key].padEnd(22)}
            {formatSetting(c.key, c.new)}
          </div>
        </div>
      ))}
      <div className="text-dim"> {facts.join(" · ")}</div>
    </div>
  );
}

function Archive({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const isAdmin = useIsAdmin();
  const archive = useArchiveService();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);

  return (
    <dl className="mt-2 grid max-w-[760px] grid-cols-[180px_1fr] gap-x-4 border-t border-line pt-3">
      <dt className="text-dim">Archive service</dt>
      <dd className="flex flex-wrap items-baseline gap-x-3">
        {confirming ? (
          <>
            <span>
              Stops {ref.service} in {ref.environment} now. Volumes and history
              are kept.
            </span>
            <Button
              className="border-bad! text-bad"
              disabled={archive.isPending}
              onClick={() =>
                archive.mutate(ref, {
                  onSuccess: () =>
                    navigate({
                      to: "/$project",
                      params: { project: ref.project },
                    }),
                })
              }
            >
              Archive {ref.service}
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
              Archive {ref.service}
            </button>
            <span className="text-dim">
              requires admin. Stops its container and removes its routes.
            </span>
          </>
        )}
      </dd>
    </dl>
  );
}
