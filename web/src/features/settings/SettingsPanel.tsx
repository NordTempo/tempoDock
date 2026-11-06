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
import type { Service, ServiceRef, SettingChange } from "../../data/types";
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
  const field = (key: TextSettingKey, shown?: string) => (
    <Editable
      serviceRef={ref}
      settingKey={key}
      value={service[key]}
      shown={shown}
      change={change(key)}
      open={edit === key}
    />
  );

  return (
    <>
      <dl className="props max-w-[860px]">
        <Item label="Image">
          <ImageValue serviceRef={ref} field={field("image")} />
        </Item>
        <Item label="Port">
          {field("port")}
          <span className="font-mono text-mut">
            {" "}
            · private {service.name}:{listenPort(service)}
          </span>
        </Item>
        <Item label="Start command">{field("start_command")}</Item>
        <Item label="Pre-deploy">
          {field("pre_deploy_command")}{" "}
          <span className="text-mut">timeout</span>{" "}
          {field("pre_deploy_timeout_seconds")}{" "}
          <PreDeploySnapshot
            serviceRef={ref}
            live={service.pre_deploy_snapshot}
            change={change("pre_deploy_snapshot")}
          />
          <span className="block text-xs text-mut">
            Runs with the new image before it gets traffic, e.g. migrations. A
            non-zero exit fails the release. With snapshots on, the
            environment's stateful services are snapshotted first.
          </span>
        </Item>
        <Item label="Health check">
          {field("health_check_path")} <span className="text-mut">timeout</span>{" "}
          {field("health_check_timeout_seconds")}
        </Item>
        <Item label="Ongoing check">
          <span className="text-mut">every</span>{" "}
          {field("health_check_interval_seconds")}
          {service.health_check_interval_seconds >= 0 && (
            <>
              {" "}
              <span className="text-mut">timeout</span>{" "}
              {field("health_check_probe_timeout_seconds")}{" "}
              <span className="text-mut">retries</span>{" "}
              {field("health_check_retries")}{" "}
              <span className="text-mut">start period</span>{" "}
              {field("health_check_start_period_seconds")}
            </>
          )}
        </Item>
        <Item label="Strategy">
          {service.stateful
            ? "stop old, then start new (a volume allows one writer)"
            : "start new next to old, then cut over"}
        </Item>
        <Item label="Limits">
          {/* Limits in the wording every tab uses (limitText). */}
          <span className="text-mut">CPU</span>{" "}
          {field("cpus", limitText(service, "cpu"))}{" "}
          <span className="text-mut">memory</span>{" "}
          {field("memory_mb", limitText(service, "memory"))}
        </Item>
        <Item label="Build">
          {field("dockerfile_path")} <span className="text-mut">command</span>{" "}
          {field("build_command")}
        </Item>
        <Item label="Volumes">
          {volumes.length === 0 ? (
            <span className="mr-4 text-mut">none</span>
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
          <AddVolume serviceRef={ref} />
        </Item>
        <Item label="Server">
          <EnvServer name={environment.server} />{" "}
          <span className="text-mut">from {environment.name}</span>
        </Item>
      </dl>
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

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
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
          label="snapshot first"
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
 * The image row: the image setting, and when it isn't what serves (a
 * failed deploy of a new tag, a rollback), the serving image above it,
 * so it is plain which one Apply keeps and which one Deploy runs.
 */
function ImageValue({
  serviceRef: ref,
  field,
}: {
  serviceRef: ServiceRef;
  field: ReactNode;
}) {
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
  if (!image.differs) {
    return (
      <span className="inline-flex flex-wrap items-center gap-x-2">
        {field}
        {failed}
      </span>
    );
  }
  return (
    <span className="flex flex-col gap-1">
      <span className="font-mono break-all">
        {image.now}{" "}
        <span className="font-sans text-ok">
          serving as r{image.serving?.number}
        </span>
      </span>
      <span className="inline-flex flex-wrap items-center gap-x-2">
        <span className="text-mut">next deploy</span>
        {field}
        {failed}
      </span>
      <span className="text-xs text-mut">
        Deploy runs the next deploy image. Applying staged changes keeps the
        serving one.
      </span>
    </span>
  );
}

/**
 * A value that edits in place, drawn like the inputs of Source below it
 * with a pencil, so it reads as editable at a glance.
 */
// A value that edits: a box with a hover border and a pointer across all
// of it, in the regular weight, so it reads as a control, not a label.
const editableBox =
  "inline-flex min-h-[24px] cursor-pointer items-center gap-1.5 rounded-ctl border px-1.5 text-left font-mono font-normal break-all hover:border-dash hover:bg-hover [&_*]:cursor-pointer";

/** The pencil inside an editable value. */
function Pencil() {
  return (
    <svg
      aria-hidden
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="flex-none text-faint"
    >
      <path d="M4 20h4L19 9l-4-4L4 16v4ZM13.5 6.5l4 4" />
    </svg>
  );
}

/**
 * A setting value in a field with a pencil: click to edit, then Stage (or
 * Enter) stages it and Cancel (or Esc) leaves it. Staged values show
 * old → new until Apply in the staged-changes bar makes them live.
 * `open` starts in the editor.
 */
function Editable({
  serviceRef: ref,
  settingKey: key,
  value,
  shown,
  change,
  open,
}: {
  serviceRef: ServiceRef;
  settingKey: TextSettingKey;
  value: string | number;
  /** The value as shown, when it reads better than formatSetting's. */
  shown?: string;
  change: KeyedChange | undefined;
  open?: boolean;
}) {
  const stage = useStageSettings();
  const discard = useDiscardChanges();
  const [draft, setDraft] = useState<string | null>(() =>
    open ? editText(key, change ? change.new : value) : null,
  );
  const [error, setError] = useState<Error | null>(null);
  const label = settingLabel[key];

  const submit = (raw: string) => {
    const patch = parseSetting(key, raw);
    if (patch instanceof Error) return setError(patch);
    setError(null);
    stage.mutate({ ref, patch }, { onSuccess: () => setDraft(null) });
  };
  const cancel = () => {
    setError(null);
    stage.reset();
    setDraft(null);
  };

  if (draft !== null) {
    return (
      <form
        aria-label={`Edit ${label}`}
        className="inline-flex max-w-full flex-col gap-1 align-top"
        onSubmit={(e) => {
          e.preventDefault();
          submit(draft);
        }}
        onKeyDown={(e) => e.key === "Escape" && cancel()}
      >
        <span className="inline-flex max-w-full flex-wrap items-center gap-2">
          <input
            // biome-ignore lint/a11y/noAutofocus: opened by clicking the value
            autoFocus
            value={draft}
            aria-label={label}
            placeholder={editPlaceholder(key)}
            onChange={(e) => setDraft(e.target.value)}
            // As wide as the value, so a long command isn't cut while typed.
            style={{ width: `${Math.max(draft.length, 18) + 3}ch` }}
            className="field min-h-[24px] max-w-full py-0.5 font-mono text-xs"
          />
          <Button small primary type="submit" disabled={stage.isPending}>
            Stage
          </Button>
          <Button small onClick={cancel}>
            Cancel
          </Button>
        </span>
        <span className="text-xs text-mut">
          A staged change goes live when you press Apply in the bar.
        </span>
        <ErrorText error={error ?? stage.error} />
      </form>
    );
  }

  return (
    <span className="inline-flex items-baseline gap-2">
      <button
        type="button"
        title={`Edit ${label}`}
        aria-label={`Edit ${label}: ${change ? formatSetting(key, change.new) : (shown ?? formatSetting(key, value).replace(" (default)", " default"))}`}
        onClick={() => {
          setError(null);
          setDraft(editText(key, change ? change.new : value));
        }}
        className={`${editableBox} ${change ? "border-warn-edge bg-stage-bg" : "border-edge bg-panel"}`}
      >
        {/* One inline box, so the spaces between its parts survive. */}
        <span>
          {change ? (
            <>
              <span className="text-warn">~</span>{" "}
              <span className="text-mut line-through">
                {formatSetting(key, change.old)}
              </span>{" "}
              {formatSetting(key, change.new)}
            </>
          ) : (
            <Shown text={shown ?? formatSetting(key, value)} />
          )}
        </span>
        <Pencil />
      </button>
      {change && (
        <>
          <span className="text-xs text-warn">staged</span>
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
        </>
      )}
      <ErrorText error={error ?? discard.error} />
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
    <section aria-labelledby="database" className="flex flex-col">
      <SectionHead id="database">Database</SectionHead>
      <dl className="props max-w-[860px]">
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
            <button
              type="button"
              title="Edit the database URL variable"
              onClick={() => setDraft(urlVariable)}
              className={`${editableBox} cursor-text border-edge bg-panel`}
            >
              {urlVariable || (
                <span className="text-mut">
                  default: DATABASE_URL, then POSTGRES_*
                </span>
              )}
              <Pencil />
            </button>
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
