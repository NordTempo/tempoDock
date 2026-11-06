import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import {
  staging,
  useAction,
  useDiscardChanges,
  useStageSettings,
} from "../../data/mutations";
import {
  envPath,
  keys,
  listenPort,
  servicePath,
  useChanges,
  useEnvironment,
  useReleases,
  useService,
  useSession,
  useVolumes,
} from "../../data/queries";
import type {
  Service,
  ServiceRef,
  SettingChange,
  SettingsPatch,
} from "../../data/types";
import { useOrg } from "../../shell/location";
import { CliHint } from "../../ui/CliHint";
import { Act, Button, Check, ErrorText, SectionHead } from "../../ui/kit";
import { SourceSection } from "../git/SourceSection";
import { EnvServer } from "../servers/parts";
import { imageSetting } from "../service/releases";
import { limitText } from "../service/Usage";
import { ApplyBox } from "../variables/ApplyBox";
import {
  editPlaceholder,
  editText,
  formatSetting,
  isSettingKey,
  parseSetting,
  type SettingKey,
  settingLabel,
  type TextSettingKey,
} from "./settings";

type KeyedChange = SettingChange & { key: SettingKey };

/**
 * Service settings, each editable in place. Edits are staged like
 * variables and go live together through the apply box, which stays in
 * view while anything is staged. `edit` opens one setting's editor.
 */
export function SettingsPanel({
  serviceRef: ref,
  edit,
}: {
  serviceRef: ServiceRef;
  edit?: TextSettingKey;
}) {
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

  const change = (key: SettingKey) => changes.find((c) => c.key === key);
  // One row of settings: its fields edit together in one small form.
  const row = (
    label: string,
    hint: string,
    parts: Part[],
    extra?: ReactNode,
  ) => (
    <SettingRow
      key={label}
      serviceRef={ref}
      label={label}
      hint={hint}
      parts={parts}
      changes={changes}
      service={service}
      open={!!edit && parts.some((p) => p.key === edit)}
      extra={extra}
    />
  );

  return (
    <>
      <section aria-labelledby="set-image">
        <SectionHead id="set-image">Image and build</SectionHead>
        <dl className="props [grid-template-columns:200px_minmax(0,1fr)]">
          {row(
            "Image",
            "What Deploy runs.",
            [{ key: "image" }],
            <ImageNote serviceRef={ref} />,
          )}
          {row("Build", "For deploys from source.", [
            { key: "dockerfile_path", word: "Dockerfile" },
            { key: "build_command", word: "build command" },
          ])}
        </dl>
      </section>

      <section aria-labelledby="set-run">
        <SectionHead id="set-run">Running</SectionHead>
        <dl className="props [grid-template-columns:200px_minmax(0,1fr)]">
          {row(
            "Port",
            "Where the app listens.",
            [{ key: "port" }],
            <span className="font-mono text-xs text-mut">
              private address {service.name}:{listenPort(service)}
            </span>,
          )}
          {row("Start command", "Instead of the image's own.", [
            { key: "start_command" },
          ])}
          {row("Limits", "The most it may use.", [
            { key: "cpus", word: "CPU", shown: limitText(service, "cpu") },
            {
              key: "memory_mb",
              word: "memory",
              shown: limitText(service, "memory"),
            },
          ])}
          <Item label="Volumes" hint="Disk that outlives releases.">
            {volumes.length === 0 ? (
              <span className="mr-4 text-mut">none</span>
            ) : (
              volumes.map((v) => (
                <span key={v.name} className="mr-4 font-mono">
                  {v.name} at {v.mount_path}
                  {v.pending && (
                    <>
                      <span className="text-warn"> saved, not applied</span>{" "}
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
            <AddVolume serviceRef={ref} />
          </Item>
          <Item label="Server" hint="Set by the environment.">
            <EnvServer name={environment.server} />{" "}
            <span className="text-mut">from {environment.name}</span>
          </Item>
        </dl>
      </section>

      <section aria-labelledby="set-release">
        <SectionHead id="set-release">Releases</SectionHead>
        <dl className="props [grid-template-columns:200px_minmax(0,1fr)]">
          {row(
            "Before traffic",
            "Runs with the new image first, like migrations.",
            [
              { key: "pre_deploy_command", word: "command" },
              { key: "pre_deploy_timeout_seconds", word: "timeout" },
            ],
            <PreDeploySnapshot
              serviceRef={ref}
              live={service.pre_deploy_snapshot}
              change={change("pre_deploy_snapshot")}
            />,
          )}
          {row("Health check", "Traffic moves once this answers.", [
            { key: "health_check_path", word: "path" },
            { key: "health_check_timeout_seconds", word: "timeout" },
          ])}
          {row(
            "While running",
            "Restarts the service when every try fails.",
            service.health_check_interval_seconds >= 0
              ? [
                  { key: "health_check_interval_seconds", word: "every" },
                  {
                    key: "health_check_probe_timeout_seconds",
                    word: "timeout",
                  },
                  { key: "health_check_retries", word: "tries" },
                  {
                    key: "health_check_start_period_seconds",
                    word: "wait after start",
                  },
                ]
              : [{ key: "health_check_interval_seconds", word: "every" }],
          )}
          <Item label="Downtime" hint="When a new release takes over.">
            {service.stateful
              ? "A few seconds: the old release stops before the new one starts, since only one copy can use the volume at a time."
              : "None: the new release starts beside the old one, then traffic moves over."}
          </Item>
        </dl>
      </section>
      <ApplyBox
        env={envPath(ref)}
        reveal={false}
        service={ref.service}
        sticky
      />

      <SourceSection serviceRef={ref} />

      {/* Only for databases: detected, or set here (so it can be undone). */}
      {(service.data_engine || service.database !== "") && (
        <DatabaseSettings serviceRef={ref} service={service} />
      )}
    </>
  );
}

/**
 * Stages a volume (POST .../volumes), applied with the environment's other
 * staged changes or, with apply, at once.
 */
const useAddVolume = () =>
  useAction(
    ({
      ref,
      ...volume
    }: {
      ref: ServiceRef;
      name: string;
      mount_path: string;
      apply: boolean;
    }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/volumes",
          { params: { path: servicePath(ref) }, body: volume },
        ),
      ),
    // The environment's subtree holds the service's volumes too.
    ({ ref }) => staging(ref),
  );

/** "+ Add volume", then a name, a mount path and whether to apply now. */
function AddVolume({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const add = useAddVolume();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [apply, setApply] = useState(false);
  if (!open) {
    return (
      <button type="button" className="lnk" onClick={() => setOpen(true)}>
        + Add volume
      </button>
    );
  }
  const cli = `dock volume add ${name.trim() || "<name>"} ${path.trim() || "<path>"}${apply ? " --deploy" : ""} -s ${ref.service} -e ${ref.environment}`;
  return (
    <form
      aria-label="Add volume"
      className="mt-1.5 flex flex-wrap items-center gap-2"
      onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { ref, name: name.trim(), mount_path: path.trim(), apply },
          {
            onSuccess: () => {
              setOpen(false);
              setName("");
              setPath("");
            },
          },
        );
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        aria-label="Volume name"
        placeholder="data"
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="field w-32 py-0.5 font-mono text-xs"
      />
      <span className="text-mut">at</span>
      <input
        aria-label="Mount path"
        placeholder="/var/lib/data"
        value={path}
        onChange={(e) => setPath(e.target.value)}
        className="field w-44 py-0.5 font-mono text-xs"
      />
      <Check label="apply now" checked={apply} onChange={setApply} />
      <Act
        small
        primary
        type="submit"
        cli={cli}
        disabled={add.isPending || !name.trim() || !path.startsWith("/")}
        title={
          path && !path.startsWith("/")
            ? "The path must be absolute"
            : undefined
        }
      >
        {apply ? "Add and apply" : "Stage volume"}
      </Act>
      <Button small onClick={() => setOpen(false)}>
        Cancel
      </Button>
      <ErrorText error={add.error} />
    </form>
  );
}

/** A settings row's label, with a short line under it saying what it is. */
function Item({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <>
      <dt>
        <span className="block text-sec">{label}</span>
        {hint && <span className="block text-2xs text-faint">{hint}</span>}
      </dt>
      <dd>{children}</dd>
    </>
  );
}

/**
 * pre_deploy_snapshot as a checkbox, staged like the other settings: on,
 * the environment's stateful services are snapshotted before the
 * pre-deploy command runs.
 */
function PreDeploySnapshot({
  serviceRef: ref,
  live,
  change,
}: {
  serviceRef: ServiceRef;
  live: boolean;
  change: KeyedChange | undefined;
}) {
  const stage = useStageSettings();
  const discard = useDiscardChanges();
  const on = change ? change.new === "true" : live;
  return (
    <span className="inline-flex items-baseline gap-2">
      <CliHint
        cli={`dock service update ${ref.service} --pre-deploy-snapshot=${!on} -e ${ref.environment}`}
        className="act"
      >
        <Check
          label="Snapshot volumes first"
          title="Snapshot the environment's stateful services before the command runs"
          checked={on}
          disabled={stage.isPending}
          onChange={(checked) =>
            stage.mutate({ ref, patch: { pre_deploy_snapshot: checked } })
          }
        />
      </CliHint>
      {change && (
        <>
          <span className="text-warn">staged</span>
          <button
            type="button"
            className="lnk"
            disabled={discard.isPending}
            onClick={() =>
              discard.mutate({
                env: envPath(ref),
                selection: {
                  settings: [{ service: ref.service, field: change.key }],
                },
              })
            }
          >
            Discard
          </button>
        </>
      )}
      <ErrorText error={stage.error ?? discard.error} />
    </span>
  );
}

/** A formatted setting with its "(default)" part quiet, so set values stand out. */
function Shown({ text }: { text: string }) {
  if (!text.endsWith(" (default)")) return text;
  return (
    <>
      {text.slice(0, -" (default)".length)}{" "}
      <span className="text-faint">default</span>
    </>
  );
}

/**
 * Under the image: when it isn't what serves (a failed deploy of a new
 * tag, a rollback), the serving image, so it is plain which one Apply
 * keeps and which one Deploy runs; and a link to a release that failed
 * with it.
 */
function ImageNote({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { service } = useService(ref);
  const image = imageSetting(service, useReleases(ref));
  const failed = image.failed && (
    <Link
      to="/$org/$project/$environment/$service/releases/$number"
      params={{ org, ...ref, number: String(image.failed.number) }}
      className="text-fail underline decoration-fail-edge underline-offset-2 hover:decoration-current"
    >
      failed as r{image.failed.number}
    </Link>
  );
  if (!image.differs) return failed || null;
  return (
    <span className="flex flex-col gap-0.5 text-xs">
      {failed}
      <span className="text-mut">
        Serving now: <span className="font-mono text-ok">{image.now}</span> as r
        {image.serving?.number}. Deploy runs the image above; applying saved
        changes keeps the serving one.
      </span>
    </span>
  );
}

/** One field in a settings row: its key, the word before it, and how to show it. */
type Part = { key: TextSettingKey; word?: string; shown?: string };

/**
 * A row of settings that edit together: the values as text, and Edit,
 * which opens a small form with one field per setting. Save stages the
 * change (it goes live with Apply in the bar under the page, like
 * variables); Cancel or Esc leaves it. Saved values show old → new until
 * applied, with Discard. `open` starts in the form (?edit=port).
 */
function SettingRow({
  serviceRef: ref,
  label,
  hint,
  parts,
  changes,
  service,
  open,
  extra,
}: {
  serviceRef: ServiceRef;
  label: string;
  hint: string;
  parts: Part[];
  changes: KeyedChange[];
  service: Service;
  open: boolean;
  extra?: ReactNode;
}) {
  const stage = useStageSettings();
  const discard = useDiscardChanges();
  const change = (key: SettingKey) => changes.find((c) => c.key === key);
  const start = () =>
    Object.fromEntries(
      parts.map(({ key }) => [
        key,
        editText(key, change(key)?.new ?? service[key]),
      ]),
    ) as Record<TextSettingKey, string>;
  const [draft, setDraft] = useState<Record<TextSettingKey, string> | null>(
    () => (open ? start() : null),
  );
  const [error, setError] = useState<Error | null>(null);
  const saved = parts.filter(({ key }) => change(key));

  const cancel = () => {
    setError(null);
    stage.reset();
    setDraft(null);
  };
  const submit = (values: Record<TextSettingKey, string>) => {
    const before = start();
    let patch: SettingsPatch = {};
    for (const { key } of parts) {
      if (values[key] === before[key]) continue;
      const one = parseSetting(key, values[key]);
      if (one instanceof Error) return setError(one);
      patch = { ...patch, ...one };
    }
    setError(null);
    if (Object.keys(patch).length === 0) return setDraft(null);
    stage.mutate({ ref, patch }, { onSuccess: () => setDraft(null) });
  };

  const value = ({ key, shown }: Part) => {
    const c = change(key);
    if (c) {
      return (
        <span className="rounded-ctl bg-stage-bg px-1">
          <span className="text-mut line-through">
            {formatSetting(key, c.old)}
          </span>{" "}
          <span className="text-warn-text">{formatSetting(key, c.new)}</span>
        </span>
      );
    }
    return <Shown text={shown ?? formatSetting(key, service[key])} />;
  };

  return (
    <Item label={label} hint={hint}>
      {draft ? (
        <form
          aria-label={`Edit ${label.toLowerCase()}`}
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            submit(draft);
          }}
          onKeyDown={(e) => e.key === "Escape" && cancel()}
        >
          <span className="flex flex-wrap items-end gap-x-3 gap-y-2">
            {parts.map(({ key, word }, i) => (
              <label key={key} className="flex min-w-0 flex-col gap-1">
                <span className="text-xs text-mut">
                  {word ?? settingLabel[key]}
                </span>
                <input
                  // biome-ignore lint/a11y/noAutofocus: opened by Edit
                  autoFocus={i === 0}
                  value={draft[key]}
                  placeholder={editPlaceholder(key)}
                  onChange={(e) =>
                    setDraft({ ...draft, [key]: e.target.value })
                  }
                  style={{
                    width: `${Math.max(draft[key].length, parts.length > 1 ? 10 : 24) + 3}ch`,
                  }}
                  className="field max-w-full py-1 font-mono text-xs"
                />
              </label>
            ))}
            <span className="flex gap-2">
              <Button small primary type="submit" disabled={stage.isPending}>
                Save
              </Button>
              <Button small onClick={cancel}>
                Cancel
              </Button>
            </span>
          </span>
          <span className="text-xs text-mut">
            Saved changes go live together when you apply them in the bar below.
            Empty means the default.
          </span>
          <ErrorText error={error ?? stage.error} />
        </form>
      ) : (
        // Edit sits at the end of a 760px line, not the far edge of a wide
        // window, so it stays near what it edits.
        <span className="flex max-w-[760px] items-start gap-3">
          <span className="flex min-w-0 flex-1 flex-col gap-1">
            <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1 font-mono break-all">
              {parts.map((part) => (
                <span key={part.key}>
                  {part.word && (
                    <span className="font-sans text-mut">{part.word} </span>
                  )}
                  {value(part)}
                </span>
              ))}
            </span>
            {extra}
          </span>
          {saved.length > 0 && (
            <button
              type="button"
              className="lnk"
              disabled={discard.isPending}
              onClick={() =>
                discard.mutate({
                  env: envPath(ref),
                  selection: {
                    settings: saved.map(({ key }) => ({
                      service: ref.service,
                      field: key,
                    })),
                  },
                })
              }
            >
              Discard
            </button>
          )}
          <Button
            small
            aria-label={`Edit ${label.toLowerCase()}`}
            onClick={() => {
              setError(null);
              setDraft(start());
            }}
          >
            Edit
          </Button>
          <ErrorText error={discard.error} />
        </span>
      )}
    </Item>
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
    <section aria-labelledby="database" className="flex flex-col">
      <SectionHead id="database">Database</SectionHead>
      <dl className="props [grid-template-columns:200px_minmax(0,1fr)]">
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
              className="field min-h-[24px] w-56 py-0.5 font-mono text-xs"
            />
          ) : canEdit ? (
            <span className="flex max-w-[760px] items-start gap-3">
              <span className="min-w-0 flex-1 font-mono">
                {urlVariable || (
                  <span className="font-sans text-mut">
                    default: DATABASE_URL, then POSTGRES_*
                  </span>
                )}
              </span>
              <Button
                small
                aria-label="Edit the database URL variable"
                onClick={() => setDraft(urlVariable)}
              >
                Edit
              </Button>
            </span>
          ) : (
            <span className="font-mono">
              {urlVariable || (
                <span className="font-sans text-mut">
                  default: DATABASE_URL, then POSTGRES_*
                </span>
              )}
            </span>
          )}
        </Item>
      </dl>
      <p className="flex flex-wrap gap-x-4 text-xs text-mut">
        <span>
          These apply at once, with no release. The Data tab reads credentials
          from the active release's variables.
        </span>
        <span className="font-mono">{cli}</span>
        <ErrorText error={save.error} />
      </p>
    </section>
  );
}
