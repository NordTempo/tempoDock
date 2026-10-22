import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import {
  useArchiveService,
  useDiscardChanges,
  useStageSettings,
} from "../../data/mutations";
import {
  envPath,
  keys,
  useChanges,
  useEnvironment,
  useIsAdmin,
  useService,
  useSession,
  useVolumes,
} from "../../data/queries";
import type { Service, ServiceRef, SettingChange } from "../../data/types";
import { Button, ErrorText, SectionHead } from "../../ui/kit";
import { SourceSection } from "../git/SourceSection";
import { EnvServer } from "../servers/parts";
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
          <EnvServer name={environment.server} />{" "}
          <span className="text-mut">from {environment.name}</span>
        </Item>
      </dl>
      <p className="text-xs text-mut">
        Click a value to edit it. Edits are staged until applied.
      </p>

      <ApplyBox env={envPath(ref)} reveal={false} />

      <SourceSection serviceRef={ref} />

      <DatabaseSettings serviceRef={ref} service={service} />

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

type DatabaseSetting = Service["database"];
type UpdateDatabaseSettings =
  components["schemas"]["UpdateDatabaseSettingsRequest"];

const databaseChoices: { value: DatabaseSetting; label: string }[] = [
  { value: "", label: "detect" },
  { value: "postgres", label: "postgres" },
  { value: "none", label: "none" },
];

/** What detection decided, in words. */
function detectedText(service: Service) {
  if (service.database === "none") return "not a database: no Data tab";
  if (service.database === "postgres") return "Postgres, set here";
  return service.data_engine
    ? "Postgres, detected from the image"
    : "not detected from the image; choose postgres if it is one";
}

/**
 * Dock-side database settings (ADR 0016 section 1): the detection override
 * and the variable with the connection URL. They apply at once, never
 * staged and never a release. Changing them needs scope deploy; others see
 * them read-only.
 */
function DatabaseSettings({
  serviceRef: ref,
  service,
}: {
  serviceRef: ServiceRef;
  service: Service;
}) {
  const scope = useSession()?.scope;
  const canEdit = scope === "deploy" || scope === "admin";
  const client = useQueryClient();
  const save = useMutation({
    mutationFn: (req: UpdateDatabaseSettings) =>
      body(
        api.PUT(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/database-settings",
          { params: { path: ref }, body: req },
        ),
      ),
    // The overview carries the service (and whether it has a Data tab).
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: keys.overview(ref.project) }),
        client.invalidateQueries({ queryKey: keys.data(ref) }),
      ]),
  });
  const [draft, setDraft] = useState<string | null>(null);
  const urlVariable = service.database_url_variable;
  const cli = [
    `dock db settings --database ${service.database || "detect"}`,
    urlVariable && `--url-variable ${urlVariable}`,
    `-s ${ref.service} -e ${ref.environment}`,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <section aria-labelledby="database" className="border-t border-line pt-4">
      <SectionHead id="database" aside="applies at once, no release">
        Database
      </SectionHead>
      <dl className="grid max-w-[860px] grid-cols-[180px_minmax(0,1fr)] gap-x-4 gap-y-2">
        <Item label="Detection">
          <span className="inline-flex flex-wrap items-baseline gap-3">
            {canEdit ? (
              <span className="seg">
                {databaseChoices.map((c) => (
                  <button
                    key={c.label}
                    type="button"
                    aria-pressed={service.database === c.value}
                    disabled={save.isPending}
                    onClick={() =>
                      service.database !== c.value &&
                      save.mutate({ database: c.value })
                    }
                  >
                    {c.label}
                  </button>
                ))}
              </span>
            ) : (
              <span className="font-mono">{service.database || "detect"}</span>
            )}
            <span className="text-mut">{detectedText(service)}</span>
          </span>
        </Item>
        <Item label="URL variable">
          {draft !== null ? (
            <input
              // biome-ignore lint/a11y/noAutofocus: opened by clicking the value
              autoFocus
              value={draft}
              aria-label="Database URL variable"
              placeholder="DATABASE_URL or api.DATABASE_URL"
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter")
                  save.mutate(
                    { database_url_variable: draft.trim() },
                    { onSuccess: () => setDraft(null) },
                  );
                if (e.key === "Escape") setDraft(null);
              }}
              onBlur={() => setDraft(null)}
              className="field w-56 py-0.5 font-mono text-xs"
            />
          ) : (
            <button
              type="button"
              title="Edit the database URL variable"
              disabled={!canEdit}
              onClick={() => setDraft(urlVariable)}
              className="font-mono enabled:hover:underline"
            >
              {urlVariable || (
                <span className="text-mut">
                  default: DATABASE_URL, then POSTGRES_*
                </span>
              )}
            </button>
          )}
        </Item>
      </dl>
      <p className="flex flex-wrap gap-x-4 text-xs text-mut">
        <span>
          The Data tab reads credentials from the active release's variables.
        </span>
        <span className="font-mono">{cli}</span>
        <ErrorText error={save.error} />
      </p>
    </section>
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
