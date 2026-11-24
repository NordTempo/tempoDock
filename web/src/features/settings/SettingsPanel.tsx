import { Link } from "@tanstack/react-router";
import { type ReactNode, useEffect, useState } from "react";
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
  useIsAdmin,
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
import { modKey } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { CliHint } from "../../ui/CliHint";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import { SectionsLayout, SettingsSection } from "../../ui/sections";
import { BackupsPanel } from "../backups/BackupsPanel";
import { SourceSection } from "../git/SourceSection";
import { EnvServer } from "../servers/parts";
import { ArchiveConfirm } from "../service/ArchiveConfirm";
import { useDiagnosis } from "../service/Diagnosis";
import { DomainsPanel } from "../service/DomainsPanel";
import { imageSetting } from "../service/releases";
import { limitText } from "../service/Usage";
import { HealthOnServer, UptimeChecks } from "../uptime/UptimePanel";
import { Folded, OpenValue } from "./OpenValue";
import { serviceSettingSections } from "./sections";
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

/** A row of a settings section, and its summary when it folds away. */
type RowItem = { key: string; node: ReactNode; folded?: string };

/** The Settings section a setting's row is in, for ?edit=<setting>. */
function sectionOf(key: TextSettingKey) {
  if (key === "image" || key === "dockerfile_path" || key === "build_command")
    return "source";
  if (key.startsWith("health_check")) return "health";
  if (key.startsWith("pre_deploy")) return "deploys";
  return "running";
}

/** Whether a stored setting is the platform default: empty, zero, off. */
const unset = (v: string | number | boolean) =>
  v === "" || v === 0 || v === false;

/**
 * A service's Settings tab: every setting on one page, in sections named
 * after what people come to change (serviceSettingSections), with the
 * list of them on the left. Rows are a label and the value; the value
 * opens its small editor. Settings left on their defaults fold into one
 * row per section. Edits are staged like variables and go live together
 * with Apply in the header. `edit` opens one setting's editor (a link
 * that points at a fix, ?edit=port).
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
  const isAdmin = useIsAdmin();
  const [archiving, setArchiving] = useState(false);
  const changes = pending.settings.flatMap((c) =>
    c.service === ref.service && isSettingKey(c.field)
      ? [{ ...c, key: c.field }]
      : [],
  );

  const change = (key: SettingKey) => changes.find((c) => c.key === key);
  const portFix = usePortFix(ref, service, change("port"));
  // ?edit=port (a failed release's fix) opens that editor: bring its
  // section into view once.
  useEffect(() => {
    if (edit) document.getElementById(sectionOf(edit))?.scrollIntoView();
  }, [edit]);
  // One row of settings: its fields edit together in one small form. It
  // folds away while every field is on its default, nothing is staged,
  // and nothing points at it (?edit, a suggested fix).
  const row = (
    label: string,
    hint: string,
    parts: Part[],
    extra?: ReactNode,
    suggest?: Suggestion,
    { fold = true, extraDefault = true } = {},
  ): RowItem => {
    const open = !!edit && parts.some((p) => p.key === edit);
    const onDefault =
      fold &&
      extraDefault &&
      !open &&
      !suggest &&
      parts.every((p) => !change(p.key) && unset(service[p.key]));
    return {
      key: label,
      node: (
        <SettingRow
          key={label}
          serviceRef={ref}
          label={label}
          hint={hint}
          parts={parts}
          changes={changes}
          service={service}
          open={open}
          staged={pending.count > 0}
          extra={extra}
          suggest={suggest}
        />
      ),
      folded: onDefault
        ? `${label.toLowerCase()} (${parts
            .map((p) =>
              (p.shown ?? formatSetting(p.key, service[p.key])).replace(
                / \(default\)$/,
                "",
              ),
            )
            .join(", ")})`
        : undefined,
    };
  };
  const item = (label: string, node: ReactNode): RowItem => ({
    key: label,
    node: (
      <Item key={label} label={label}>
        {node}
      </Item>
    ),
  });
  const sections = serviceSettingSections
    .filter((s) => !("stateful" in s) || service.stateful)
    .map((s) =>
      s.id === "archive" ? { ...s, label: `Archive ${ref.service}` } : s,
    );

  return (
    <SectionsLayout label={`${ref.service} settings`} sections={sections}>
      <div className="flex flex-col gap-10 md:pl-8">
        <SettingsSection id="source" title="Source">
          <Rows
            items={[
              row(
                "Image",
                "What Deploy runs. Empty: it builds from source.",
                [{ key: "image" }],
                <ImageNote serviceRef={ref} />,
                undefined,
                { fold: false },
              ),
              row("Build", "For deploys from source.", [
                { key: "dockerfile_path", word: "Dockerfile" },
                { key: "build_command", word: "build command" },
              ]),
            ]}
          />
          <SourceSection serviceRef={ref} />
        </SettingsSection>

        <SettingsSection id="running" title="Running">
          <Rows
            items={[
              row(
                "Port",
                "Where the app listens.",
                // Unset, it is the template's port (5432, 6379) or 8080.
                [
                  {
                    key: "port",
                    shown: service.port
                      ? String(service.port)
                      : `${listenPort(service)} (default)`,
                  },
                ],
                <span className="font-mono text-xs text-mut">
                  others reach it at {service.name}:{listenPort(service)}
                </span>,
                portFix,
                { fold: false },
              ),
              row("Start command", "Instead of the image's own.", [
                { key: "start_command" },
              ]),
              row("Limits", "The most it may use.", [
                { key: "cpus", word: "CPU", shown: limitText(service, "cpu") },
                {
                  key: "memory_mb",
                  word: "memory",
                  shown: limitText(service, "memory"),
                },
              ]),
              item(
                "Volumes",
                <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
                  {volumes.length === 0 ? (
                    <span className="text-mut">none</span>
                  ) : (
                    volumes.map((v) => (
                      <span key={v.name} className="font-mono">
                        {v.name} at {v.mount_path}
                        {v.pending && (
                          <>
                            <span className="font-sans text-warn">
                              {" "}
                              saved, not applied
                            </span>{" "}
                            <button
                              type="button"
                              className="lnk font-sans"
                              disabled={discard.isPending}
                              onClick={() =>
                                discard.mutate({
                                  env: envPath(ref),
                                  selection: {
                                    volumes: [
                                      { service: ref.service, name: v.name },
                                    ],
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
                </span>,
              ),
              ...(environment.server
                ? [
                    item(
                      "Server",
                      <>
                        <EnvServer environment={environment} />{" "}
                        <span className="text-mut">
                          set by {environment.name}
                        </span>
                      </>,
                    ),
                  ]
                : []),
            ]}
          />
          {/* Only for databases: detected, or set here (so it can be undone). */}
          {(service.data_engine || service.database !== "") && (
            <DatabaseSettings serviceRef={ref} service={service} />
          )}
        </SettingsSection>

        <SettingsSection id="deploys" title="Deploys">
          <Rows
            items={[
              item(
                "Downtime",
                service.stateful
                  ? "A few seconds: the old release stops before the new one starts, since only one copy can use the volume at a time."
                  : "None: the new release starts beside the old one, then traffic moves over.",
              ),
              row(
                "Before it goes live",
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
                undefined,
                {
                  extraDefault:
                    !service.pre_deploy_snapshot &&
                    !change("pre_deploy_snapshot"),
                },
              ),
            ]}
          />
        </SettingsSection>

        <SettingsSection id="health" title="Health and uptime">
          <Rows
            items={[
              row(
                "Health check",
                "Traffic moves to a new release once this answers.",
                [
                  { key: "health_check_path", word: "path" },
                  { key: "health_check_timeout_seconds", word: "timeout" },
                ],
                undefined,
                undefined,
                { fold: false },
              ),
              row(
                "Checks while running",
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
              ),
            ]}
          />
          <HealthOnServer serviceRef={ref} />
          <UptimeChecks serviceRef={ref} />
        </SettingsSection>

        <DomainsPanel serviceRef={ref} />

        {service.stateful && <BackupsPanel serviceRef={ref} />}

        <SettingsSection id="archive" title={`Archive ${ref.service}`}>
          <p className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sec">
            <span>
              Stops it and takes its domains down. Volumes, variables and
              releases are kept, listed under Archived on the project.
            </span>
            {isAdmin ? (
              <Button danger onClick={() => setArchiving(true)}>
                Archive {ref.service}…
              </Button>
            ) : (
              <span className="text-mut">An admin can archive it.</span>
            )}
          </p>
          {archiving && (
            <ArchiveConfirm
              serviceRef={ref}
              onCancel={() => setArchiving(false)}
            />
          )}
        </SettingsSection>
      </div>
    </SectionsLayout>
  );
}

/**
 * A section's rows: the set ones, then the ones on their defaults folded
 * into one "N on defaults" row that opens them in place.
 */
function Rows({ items }: { items: RowItem[] }) {
  const [open, setOpen] = useState(false);
  const folded = items.filter((i) => i.folded);
  return (
    <dl className="props [grid-template-columns:200px_minmax(0,1fr)]">
      {items.filter((i) => !i.folded).map((i) => i.node)}
      <Folded
        summary={folded.map((i) => i.folded)}
        open={open}
        onToggle={() => setOpen(!open)}
      />
      {open && folded.map((i) => i.node)}
    </dl>
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
      <Button small onClick={() => setOpen(true)}>
        Add volume
      </Button>
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

/** A settings row: its label, and the value (or what opens its editor). */
function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>
        <span className="block text-sec">{label}</span>
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
      to="/$org/$project/$environment/$service/history/$number"
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

/** A value the evidence suggests for a row's field, with why: "Use 8080". */
type Suggestion = { key: TextSettingKey; value: string; why: string };

/**
 * The port the app said it listened on, when the last release that ran
 * the port about to be used (staged, or the setting) failed its health
 * check because the app listened elsewhere: the deploy diagnosis' fix
 * (ADR 0071, rule port_mismatch). Undefined otherwise.
 */
function usePortFix(
  ref: ServiceRef,
  service: Service,
  staged: KeyedChange | undefined,
): Suggestion | undefined {
  const port = staged ? Number(staged.new) : service.port;
  const last = useReleases(ref).find((r) => r.config.port === port);
  const failed =
    last?.status === "failed" && last.diagnosis?.rule === "port_mismatch"
      ? last
      : undefined;
  const fix = useDiagnosis(ref, failed)?.cause.fix;
  if (!failed || fix?.setting !== "port" || !fix.suggested) return undefined;
  if (fix.suggested === String(port)) return undefined;
  return {
    key: "port",
    value: fix.suggested,
    why: `The app listened on ${fix.suggested} in r${failed.number}`,
  };
}

/**
 * A row of settings that edit together: the values as text; a click on
 * them opens a small form with one field per setting, and `hint` (what
 * the setting does) over it. Save stages the
 * change (it goes live with Apply in the bar under the page, like
 * variables); Cancel or Esc leaves it. Saved values show old → new until
 * applied, with Discard. `open` starts in the form (?edit=port). The
 * form names Apply only once the environment has staged changes
 * (`staged`), when the header shows it. `suggest`, open or not, stages
 * a value the evidence suggests in one click.
 */
function SettingRow({
  serviceRef: ref,
  label,
  hint,
  parts,
  changes,
  service,
  open,
  staged,
  extra,
  suggest,
}: {
  serviceRef: ServiceRef;
  label: string;
  hint: string;
  parts: Part[];
  changes: KeyedChange[];
  service: Service;
  open: boolean;
  staged: boolean;
  extra?: ReactNode;
  suggest?: Suggestion;
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
  const suggestion = suggest && (
    <span className="text-xs text-warn-text">
      {suggest.why}.{" "}
      <button
        type="button"
        className="lnk"
        disabled={stage.isPending}
        onClick={() =>
          submit({ ...(draft ?? start()), [suggest.key]: suggest.value })
        }
      >
        Use {suggest.value}
      </button>
    </span>
  );

  return (
    <Item label={label}>
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
          <span className="text-xs text-mut">{hint}</span>
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
          {suggestion}
          <span className="text-xs text-mut">
            {staged &&
              `Saved changes wait for Apply in the header (${modKey.trim()} Enter), which puts them live together. `}
            Empty means the default.
          </span>
          <ErrorText error={error ?? stage.error} />
        </form>
      ) : (
        <span className="flex flex-col gap-1">
          <OpenValue
            label={`Edit ${label.toLowerCase()}`}
            onOpen={() => {
              setError(null);
              setDraft(start());
            }}
          >
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
          </OpenValue>
          {saved.length > 0 && (
            <span className="flex flex-wrap items-baseline gap-3 text-xs text-mut">
              Saved: Apply in the header puts it live.
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
            </span>
          )}
          {suggestion}
          {extra}
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
  { value: "mysql", label: "mysql" },
  { value: "redis", label: "redis" },
  { value: "none", label: "none" },
];

const engineNames: Record<NonNullable<Service["data_engine"]>, string> = {
  postgres: "Postgres",
  mysql: "MySQL or MariaDB",
  redis: "Redis or Valkey",
};

/** What detection decided, in words. */
function detectedText(service: Service) {
  if (service.database === "none") return "not a database: no Data tab";
  if (service.database) return `${engineNames[service.database]}, set here`;
  return service.data_engine
    ? `${engineNames[service.data_engine]}, detected from the image`
    : "not detected from the image; choose its engine if it is one";
}

/**
 * Dock-side database settings (ADR 0016 section 1), as rows under
 * Running: the detection override and the variable with the connection
 * URL. They apply at once, never staged and never a release. Changing
 * them needs scope deploy; others see them read-only.
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
  const save = useAction(
    (req: UpdateDatabaseSettings) =>
      body(
        api.PUT(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/database-settings",
          { params: { path: ref }, body: req },
        ),
      ),
    // The overview carries the service (and whether it has a Data tab).
    () => [keys.overview(ref.project), keys.data(ref), ["audit"]],
  );
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
    <div className="flex flex-col">
      <dl className="props border-t-0 [grid-template-columns:200px_minmax(0,1fr)]">
        <Item label="Database">
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
          ) : (
            <OpenValue
              label="Edit the database URL variable"
              onOpen={canEdit ? () => setDraft(urlVariable) : undefined}
            >
              <span className="font-mono">
                {urlVariable || (
                  <span className="font-sans text-mut">
                    default: DATABASE_URL, then POSTGRES_*
                  </span>
                )}
              </span>
            </OpenValue>
          )}
        </Item>
      </dl>
      <p className="flex flex-wrap gap-x-4 pt-2 text-xs text-mut">
        <span>
          Database settings apply at once, with no release. The Data tab reads
          credentials from the active release's variables.
        </span>
        <span className="font-mono">{cli}</span>
        <ErrorText error={save.error} />
      </p>
    </div>
  );
}
