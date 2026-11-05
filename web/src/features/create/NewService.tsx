import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { queries, useEnvironment } from "../../data/queries";
import type { EnvRef, Service, TemplateInfo } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Button, CopyButton, ErrorText } from "../../ui/kit";
import {
  autofocus,
  Choice,
  Field,
  Fields,
  Footer,
  Group,
  input,
  nameProblem,
  toName,
} from "./form";
import {
  type GitConnected,
  type NewService as Request,
  useConnectGit,
  useCreateService,
} from "./mutations";
import type { ServiceSource } from "./open";

const sources = [
  ["image", "Image"],
  ["source", "Source"],
  ["github", "GitHub"],
  ["database", "Database"],
] as const;

/** The advanced settings, as typed. Empty means the default. */
type Advanced = {
  start: string;
  build: string;
  dockerfile: string;
  healthPath: string;
  healthTimeout: string;
  cpus: string;
  memory: string;
};

const noAdvanced: Advanced = {
  start: "",
  build: "",
  dockerfile: "",
  healthPath: "",
  healthTimeout: "",
  cpus: "",
  memory: "",
};

/** A whole number in [0, max], or undefined (with a problem) when not. */
const whole = (v: string, max = Number.MAX_SAFE_INTEGER) => {
  const n = Number(v);
  return v === "" || (Number.isInteger(n) && n >= 0 && n <= max)
    ? n
    : undefined;
};

/** Single-quotes a CLI argument when the shell would split it. */
const arg = (v: string) =>
  /^[\w@%+=:,./-]+$/.test(v) ? v : `'${v.replaceAll("'", `'\\''`)}'`;

type Done =
  | { kind: "database"; service: Service; reference: string }
  | { kind: "github"; service: Service; git: GitConnected }
  | { kind: "unconnected"; service: Service; error: Error };

/**
 * `dock service create` with every flag: from an image, from source (built
 * on deploy), from a GitHub repository (created, then connected like
 * `dock git connect`), or a database from a template. Opens the service,
 * except where the answer has something to keep: a database's variable
 * reference, a webhook's secret.
 */
export function NewService({
  env,
  source: initial = "image",
  onClose,
}: {
  env: EnvRef;
  source?: ServiceSource;
  onClose: () => void;
}) {
  const org = useOrg();
  const { services } = useEnvironment(env);
  const templates = useQuery(queries.templates());
  const create = useCreateService();
  const connect = useConnectGit();
  const navigate = useNavigate();

  const [source, setSource] = useState<ServiceSource>(initial);
  const [typed, setTyped] = useState<string | null>(null);
  const [image, setImage] = useState("");
  const [port, setPort] = useState("");
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [dir, setDir] = useState("");
  const [token, setToken] = useState("");
  const [template, setTemplate] = useState<TemplateInfo["name"]>("postgres");
  const [adv, setAdv] = useState(noAdvanced);
  const [done, setDone] = useState<Done | null>(null);

  const database = source === "database";
  const built = source === "source" || source === "github";
  // A database is named after its template until a name is typed.
  const name = typed ?? (database ? template : "");
  const info = templates.data?.find((t) => t.name === template);

  const portN = whole(port, 65535);
  const timeout = whole(adv.healthTimeout);
  const memory = whole(adv.memory);
  const cpus = adv.cpus === "" ? 0 : Number(adv.cpus);
  const problem =
    nameProblem(
      "service",
      name,
      services.map((s) => s.service.name),
    ) ??
    (!database && portN === undefined
      ? "Port is a whole number up to 65535; leave it empty for 8080."
      : undefined) ??
    (adv.healthPath && !adv.healthPath.startsWith("/")
      ? "The health check path starts with /, like /healthz."
      : undefined) ??
    (timeout === undefined || memory === undefined || !(cpus >= 0)
      ? "Timeout, CPUs and memory are positive numbers; leave them empty for no limit."
      : undefined);
  const missing =
    !name ||
    (source === "image" && !image.trim()) ||
    (source === "github" && !/^[\w.-]+\/[\w.-]+$/.test(repo.trim())) ||
    (database && !info);

  if (done) {
    return <Created env={env} done={done} onClose={onClose} />;
  }

  // The request: template only for a database; build settings only when
  // built from source; empty fields left out.
  const settings: Omit<Request, "name"> = database
    ? { template }
    : {
        ...(source === "image" && { image: image.trim() }),
        ...(portN && { port: portN }),
        ...(adv.start && { start_command: adv.start }),
        ...(built && adv.build && { build_command: adv.build }),
        ...(built && adv.dockerfile && { dockerfile_path: adv.dockerfile }),
        ...(adv.healthPath && { health_check_path: adv.healthPath }),
        ...(timeout && { health_check_timeout_seconds: timeout }),
        ...(cpus && { cpus }),
        ...(memory && { memory_mb: memory }),
      };

  const where = `-e ${env.environment} -p ${env.project}`;
  const flags = Object.entries({
    template: settings.template,
    image: settings.image,
    port: settings.port,
    "start-command": settings.start_command,
    "build-command": settings.build_command,
    dockerfile: settings.dockerfile_path,
    "health-check-path": settings.health_check_path,
    "health-check-timeout":
      settings.health_check_timeout_seconds &&
      `${settings.health_check_timeout_seconds}s`,
    cpus: settings.cpus,
    memory: settings.memory_mb,
  })
    .filter(([, v]) => v)
    .map(([flag, v]) => ` --${flag} ${arg(String(v))}`)
    .join("");
  const cli = [`dock service create ${name || "<name>"}${flags} ${where}`];
  if (source === "github") {
    cli.push(
      `dock git connect ${repo.trim() || "owner/repo"} --branch ${arg(branch.trim() || "main")}${dir.trim() ? ` --dir ${arg(dir.trim())}` : ""}${token ? " --token-stdin" : ""} -s ${name || "<name>"} ${where}`,
    );
  }

  async function submit() {
    const service = await create.mutateAsync({ env, name, ...settings });
    if (database) {
      const exported = info?.exports[0];
      setDone({
        kind: "database",
        service,
        reference: exported ? `\${{${service.name}.${exported}}}` : "",
      });
    } else if (source === "github") {
      try {
        const git = await connect.mutateAsync({
          env,
          service: service.name,
          repo: repo.trim(),
          branch: branch.trim() || "main",
          dir: dir.trim(),
          ...(token && { token }),
        });
        setDone({ kind: "github", service, git });
      } catch (e) {
        const error = e instanceof Error ? e : new Error(String(e));
        setDone({ kind: "unconnected", service, error });
      }
    } else {
      await navigate({
        to: "/$org/$project/$environment/$service",
        params: { org, ...env, service: service.name },
      });
      onClose();
    }
  }

  const set = (key: keyof Advanced) => (v: string) =>
    setAdv((a) => ({ ...a, [key]: v }));

  return (
    <form
      aria-label="New service"
      onSubmit={(e) => {
        e.preventDefault();
        // Failures show from the mutations' state.
        submit().catch(() => {});
      }}
    >
      <Fields>
        <Field label="Name">
          <input
            ref={autofocus}
            value={name}
            onChange={(e) => setTyped(toName(e.target.value))}
            placeholder={database ? template : "web"}
            spellCheck={false}
            autoComplete="off"
            className={input}
          />
        </Field>
        <Group label="Source">
          <Choice
            label="Source"
            name="source"
            value={source}
            options={sources}
            onChange={(next) => {
              setSource(next);
              create.reset();
            }}
          />
        </Group>

        {source === "image" && (
          <Field
            label="Image"
            note="Deploys pull it again, so a moving tag stays up to date."
          >
            <Text
              value={image}
              onChange={setImage}
              placeholder="ghcr.io/acme/api:1.4"
            />
          </Field>
        )}
        {source === "source" && (
          <span className="col-start-2 text-xs text-mut">
            Built on deploy, with its Dockerfile or detected by Railpack. Deploy
            a directory with{" "}
            <code className="font-mono text-sec">
              dock deploy -s {name || "<name>"} {where}
            </code>
            .
          </span>
        )}
        {source === "github" && (
          <>
            <Field label="Repository">
              <Text value={repo} onChange={setRepo} placeholder="owner/repo" />
            </Field>
            <Field label="Branch" note="Every push to it deploys.">
              <Text value={branch} onChange={setBranch} placeholder="main" />
            </Field>
            <Field label="Directory">
              <Text
                value={dir}
                onChange={setDir}
                placeholder="repository root"
              />
            </Field>
            <Field
              label="Token"
              note="For a private repository: fine-grained, Contents read. Stored encrypted."
            >
              <input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="public repository"
                autoComplete="off"
                className={input}
              />
            </Field>
          </>
        )}
        {database && (
          <Group
            label="Template"
            note={
              info &&
              `${info.description}, a volume at ${info.mount_path}, generated credentials, daily backups. Deploys right away.`
            }
          >
            <Choice
              label="Template"
              name="template"
              value={template}
              options={(templates.data ?? []).map(
                (t) => [t.name, t.name] as const,
              )}
              onChange={setTemplate}
            />
          </Group>
        )}
        {!database && (
          <Field label="Port">
            <Text
              value={port}
              onChange={setPort}
              placeholder="8080"
              inputMode="numeric"
              width="w-24"
            />
          </Field>
        )}
      </Fields>

      {!database && (
        <details className="mt-3">
          <summary className="cursor-pointer text-xs text-mut hover:text-fg">
            Advanced
          </summary>
          <div className="mt-2.5">
            <Fields>
              <Field label="Start command">
                <Text
                  value={adv.start}
                  onChange={set("start")}
                  placeholder="the image's"
                />
              </Field>
              {built && (
                <>
                  <Field label="Dockerfile">
                    <Text
                      value={adv.dockerfile}
                      onChange={set("dockerfile")}
                      placeholder="Dockerfile, if there is one"
                    />
                  </Field>
                  <Field label="Build command">
                    <Text
                      value={adv.build}
                      onChange={set("build")}
                      placeholder="detected (Railpack, without a Dockerfile)"
                    />
                  </Field>
                </>
              )}
              <Field label="Health check">
                <span className="flex flex-wrap items-center gap-2">
                  <Text
                    value={adv.healthPath}
                    onChange={set("healthPath")}
                    placeholder="TCP check"
                    width="w-40"
                    label="Health check path"
                  />
                  <Text
                    value={adv.healthTimeout}
                    onChange={set("healthTimeout")}
                    placeholder="timeout"
                    inputMode="numeric"
                    width="w-24"
                    label="Health check timeout in seconds"
                  />
                  <span className="text-xs text-mut">seconds</span>
                </span>
              </Field>
              <Field label="Limits">
                <span className="flex flex-wrap items-center gap-2">
                  <Text
                    value={adv.cpus}
                    onChange={set("cpus")}
                    placeholder="no limit"
                    inputMode="decimal"
                    width="w-24"
                    label="CPUs"
                  />
                  <span className="text-xs text-mut">CPUs</span>
                  <Text
                    value={adv.memory}
                    onChange={set("memory")}
                    placeholder="no limit"
                    inputMode="numeric"
                    width="w-28"
                    label="Memory in MB"
                  />
                  <span className="text-xs text-mut">MB memory</span>
                </span>
              </Field>
            </Fields>
          </div>
        </details>
      )}

      {problem && <p className="mt-3 text-fail">{problem}</p>}
      <Footer
        submit={database ? "Create database" : "Create service"}
        pending={create.isPending || connect.isPending}
        disabled={missing || !!problem}
        error={templates.error ?? create.error}
        cli={cli}
        onClose={onClose}
      />
    </form>
  );
}

/** A one-line text field in the dialog's mono style. */
function Text({
  value,
  onChange,
  placeholder,
  inputMode,
  width = "w-full",
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  inputMode?: "numeric" | "decimal";
  width?: string;
  /** For fields that share a row and so a label. */
  label?: string;
}) {
  return (
    <input
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      inputMode={inputMode}
      spellCheck={false}
      autoComplete="off"
      className={`${input} ${width}`}
    />
  );
}

/** What was created, and what to keep from the answer. */
function Created({
  env,
  done,
  onClose,
}: {
  env: EnvRef;
  done: Done;
  onClose: () => void;
}) {
  const org = useOrg();
  const { service } = done;
  const ref = { ...env, service: service.name };
  const open = (
    <Link
      to="/$org/$project/$environment/$service"
      params={{ org, ...ref }}
      className="btn pri"
      ref={autofocus}
      onClick={onClose}
    >
      Open {service.name}
    </Link>
  );
  if (done.kind === "database") {
    return (
      <div className="flex flex-col gap-3">
        <p>
          Created <span className="font-mono">{service.name}</span>, deploying.
        </p>
        {done.reference && (
          <p className="flex flex-wrap items-center gap-2">
            Other services use{" "}
            <code className="font-mono">{done.reference}</code>
            <CopyButton text={done.reference} />
          </p>
        )}
        <div className="flex gap-2">
          {open}
          <Button onClick={onClose}>Done</Button>
        </div>
      </div>
    );
  }
  if (done.kind === "unconnected") {
    return (
      <div className="flex flex-col gap-3">
        <p>
          Created <span className="font-mono">{service.name}</span>, but
          connecting the repository failed. Fix it and connect again on its
          Build & run tab.
        </p>
        <ErrorText error={done.error} />
        <div className="flex gap-2">
          <Link
            to="/$org/$project/$environment/$service/build"
            params={{ org, ...ref }}
            className="btn pri"
            ref={autofocus}
            onClick={onClose}
          >
            Open Build & run
          </Link>
          <Button onClick={onClose}>Close</Button>
        </div>
      </div>
    );
  }
  const { git } = done;
  const conn = git.connection;
  return (
    <div className="flex flex-col gap-3">
      <p>
        Created <span className="font-mono">{service.name}</span>, connected to{" "}
        <span className="font-mono">
          {conn.repo}@{conn.branch}
        </span>
        . On GitHub, open{" "}
        <a
          href={`https://github.com/${conn.repo}/settings/hooks/new`}
          target="_blank"
          rel="noreferrer"
        >
          Settings → Webhooks → Add webhook
        </a>{" "}
        and set:
      </p>
      <dl className="props">
        <Item label="Payload URL">
          {conn.webhook_url ? (
            <Copyable text={conn.webhook_url} />
          ) : (
            <span className="text-warn">none yet</span>
          )}
        </Item>
        <Item label="Content type">
          <span className="font-mono">application/json</span>
        </Item>
        {git.secret && (
          <Item label="Secret">
            <Copyable text={git.secret} />
          </Item>
        )}
        <Item label="Events">Just the push event</Item>
      </dl>
      {git.hooks_hint && <p className="text-xs text-warn">{git.hooks_hint}</p>}
      {git.secret && (
        <p className="text-warn">The secret is not shown again.</p>
      )}
      <div className="flex gap-2">{open}</div>
    </div>
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

function Copyable({ text }: { text: string }) {
  return (
    <span className="inline-flex flex-wrap items-baseline gap-2">
      <span className="font-mono break-all">{text}</span>
      <CopyButton text={text} />
    </span>
  );
}
