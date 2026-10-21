import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  useArchiveService,
  useDiscardChanges,
  useStageSettings,
} from "../../data/mutations";
import {
  envPath,
  useChanges,
  useEnvironment,
  useIsAdmin,
  useService,
  useVolumes,
} from "../../data/queries";
import type { ServiceRef, SettingChange } from "../../data/types";
import { Button, ErrorText, SectionHead } from "../../ui/kit";
import { ApplyBox } from "../variables/ApplyBox";
import {
  editText,
  formatSetting,
  isSettingKey,
  parseSetting,
  type SettingKey,
  settingLabel,
} from "./settings";

type KeyedChange = SettingChange & { key: SettingKey };

/**
 * Service settings, each editable in place. Edits are staged like
 * variables and go live together through the apply box.
 */
export function SettingsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const { environment } = useEnvironment(ref);
  const volumes = useVolumes(ref);
  const pending = useChanges(ref);
  const discard = useDiscardChanges();
  const changes = pending.settings.flatMap((c) =>
    c.service === ref.service && isSettingKey(c.field)
      ? [{ ...c, key: c.field }]
      : [],
  );

  const field = (key: SettingKey) => (
    <Editable
      serviceRef={ref}
      settingKey={key}
      value={service[key]}
      change={changes.find((c) => c.key === key)}
    />
  );

  return (
    <>
      <dl className="grid max-w-[860px] grid-cols-[180px_minmax(0,1fr)] gap-x-4 gap-y-2">
        <Item label="Image">{field("image")}</Item>
        <Item label="Port">
          {field("port")}
          <span className="font-mono text-mut">
            {" "}
            · private {service.name}:{service.port || 8080}
          </span>
        </Item>
        <Item label="Start command">{field("start_command")}</Item>
        <Item label="Health check">
          {field("health_check_path")} <span className="text-mut">timeout</span>{" "}
          {field("health_check_timeout_seconds")}
        </Item>
        <Item label="Strategy">
          {service.stateful
            ? "stop old, then start new (a volume allows one writer)"
            : "start new next to old, then cut over"}
        </Item>
        <Item label="Limits">
          <span className="text-mut">CPU</span> {field("cpus")}{" "}
          <span className="text-mut">memory</span> {field("memory_mb")}
        </Item>
        <Item label="Build">
          {field("dockerfile_path")} <span className="text-mut">command</span>{" "}
          {field("build_command")}
        </Item>
        <Item label="Volumes">
          {volumes.length === 0 ? (
            <span className="text-mut">none</span>
          ) : (
            volumes.map((v) => (
              <span key={v.name} className="mr-4 font-mono">
                {v.name} at {v.mount_path}
                {v.pending && (
                  <>
                    <span className="text-warn"> staged</span>{" "}
                    <button
                      type="button"
                      className="lnk font-sans"
                      disabled={discard.isPending}
                      onClick={() =>
                        discard.mutate({
                          env: envPath(ref),
                          selection: {
                            volumes: [{ service: ref.service, name: v.name }],
                          },
                        })
                      }
                    >
                      Discard
                    </button>
                  </>
                )}
              </span>
            ))
          )}
        </Item>
        <Item label="Server">
          <span className="font-mono">{environment.server}</span>{" "}
          <span className="text-mut">from {environment.name}</span>
        </Item>
      </dl>
      <p className="text-xs text-mut">
        Click a value to edit it. Edits are staged until applied.
      </p>

      <ApplyBox env={envPath(ref)} reveal={false} />

      <Archive serviceRef={ref} />
    </>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-mut">{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

/** A setting value; click to edit. Staged values show old → new. */
function Editable({
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
  const discard = useDiscardChanges();
  const [draft, setDraft] = useState<string | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const submit = (raw: string) => {
    const patch = parseSetting(key, raw);
    if (patch instanceof Error) return setError(patch);
    setError(null);
    stage.mutate({ ref, patch }, { onSuccess: () => setDraft(null) });
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
            if (e.key === "Enter") submit(draft);
            if (e.key === "Escape") setDraft(null);
          }}
          onBlur={() => setDraft(null)}
          className="field w-56 py-0.5 font-mono text-xs"
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
        className={`font-mono hover:underline ${change ? "bg-stage-bg px-1" : ""}`}
      >
        {change ? (
          <>
            <span className="text-warn">~</span>{" "}
            <span className="text-mut line-through">
              {formatSetting(key, change.old)}
            </span>{" "}
            {formatSetting(key, change.new)}
          </>
        ) : (
          formatSetting(key, value)
        )}
      </button>
      {change && (
        <button
          type="button"
          className="lnk"
          disabled={discard.isPending}
          onClick={() =>
            discard.mutate({
              env: envPath(ref),
              selection: { settings: [{ service: ref.service, field: key }] },
            })
          }
        >
          Discard
        </button>
      )}
      <ErrorText error={error ?? stage.error ?? discard.error} />
    </span>
  );
}

function Archive({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const isAdmin = useIsAdmin();
  const archive = useArchiveService();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  if (!isAdmin) return null;
  return (
    <section aria-labelledby="archive" className="border-t border-line pt-4">
      <SectionHead id="archive">Archive {ref.service}</SectionHead>
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-mut">
          Stops its container and removes its routes in {ref.environment}.
          Volumes and history are kept.
        </span>
        {confirming ? (
          <>
            <Button
              danger
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
              Archive {ref.service} now
            </Button>
            <Button onClick={() => setConfirming(false)}>Cancel</Button>
          </>
        ) : (
          <Button danger onClick={() => setConfirming(true)}>
            Archive
          </Button>
        )}
        <ErrorText error={archive.error} />
      </div>
    </section>
  );
}
