import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useDeploy } from "../../data/mutations";
import {
  canDeploy,
  useEnvironment,
  useProjectOverview,
  useSession,
} from "../../data/queries";
import type { Environment, EnvRef, Service } from "../../data/types";
import { EnvButtons } from "../../shell/EnvButtons";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { plural } from "../../ui/format";
import { Button, CopyButton, ErrorText } from "../../ui/kit";
import { useGitHubRepos } from "../git/api";
import { FromTemplate } from "../templates/FromTemplate";
import { FromGitHub } from "./FromGitHub";
import {
  autofocus,
  Choice,
  Field,
  Fields,
  Footer,
  Group,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
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
  // "Folder", as the Deploy window says: code from this machine.
  ["source", "Folder"],
  ["github", "GitHub"],
  ["template", "Template"],
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
  | { kind: "github"; service: Service; git: GitConnected }
  | { kind: "unconnected"; service: Service; error: Error };

/**
 * `dock service create` with every flag: from an image, from source (built
 * on deploy), from a GitHub repository (created, then connected like
 * `dock git connect`), or `dock template use` for an app or a database
 * (FromTemplate, which widens the dialog). An image deploys right after it
 * is created ("Create, don't deploy" skips that). Opens the service,
 * except where the answer has something to keep: a webhook's secret. Submitting with fields missing
 * marks them instead of doing nothing (useValidation). The environment
 * starts as the one it was opened from and can be changed in the form.
 */
export function NewService({
  env: opened,
  source: initial = "image",
  template,
  onClose,
}: {
  env: EnvRef;
  source?: ServiceSource;
  template?: string;
  onClose: () => void;
}) {
  const [environment, setEnvironment] = useState(opened.environment);
  const [source, setSource] = useState<ServiceSource>(initial);
  const env = { project: opened.project, environment };
  const session = useSession();
  const choices = useProjectOverview(env.project)
    .environments.map((e) => e.environment)
    .filter((e) => canDeploy(session, { ...env, environment: e.name }));
  const title = `New service in ${env.project} ${environment}`;
  return (
    <Dialog
      title={title}
      onClose={onClose}
      wide
      className={source === "template" ? "m-auto max-w-[900px]" : undefined}
    >
      <ServiceForm
        env={env}
        choices={choices}
        onEnvironment={setEnvironment}
        source={source}
        setSource={setSource}
        template={template}
        onClose={onClose}
      />
    </Dialog>
  );
}

function ServiceForm({
  env,
  choices,
  onEnvironment,
  source,
  setSource,
  template,
  onClose,
}: {
  env: EnvRef;
  /** The environments the caller may add a service to. */
  choices: readonly Environment[];
  onEnvironment: (environment: string) => void;
  source: ServiceSource;
  setSource: (source: ServiceSource) => void;
  template?: string;
  onClose: () => void;
}) {
  const org = useOrg();
  const { services } = useEnvironment(env);
  const create = useCreateService();
  const connect = useConnectGit();
  const deploy = useDeploy();
  const navigate = useNavigate();
  const v = useValidation();

  // The org's repositories through its GitHub App, once GitHub is picked.
  const repos = useGitHubRepos(source === "github");
  const [typed, setTyped] = useState<string | null>(null);
  const [image, setImage] = useState("");
  const [port, setPort] = useState("");
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [dir, setDir] = useState("");
  const [token, setToken] = useState("");
  const [adv, setAdv] = useState(noAdvanced);
  const [done, setDone] = useState<Done | null>(null);

  const built = source === "source" || source === "github";
  const name = typed ?? "";

  const portN = whole(port, 65535);
  const timeout = whole(adv.healthTimeout);
  const memory = whole(adv.memory);
  const cpus = adv.cpus === "" ? 0 : Number(adv.cpus);
  // Wrong as typed, under their fields.
  const nameError =
    nameProblem(
      "service",
      name,
      services.map((s) => s.service.name),
    ) ?? v.required(!name, "Enter a name, like web.");
  const imageError = v.required(
    source === "image" && !image.trim(),
    "Enter the image to run, with its tag.",
  );
  const repoValid = /^[\w.-]+\/[\w.-]+$/.test(repo.trim());
  const repoError = v.required(
    source === "github" && !repoValid,
    "Enter the repository as owner/repo.",
  );
  const portError =
    portN === undefined
      ? "A whole number up to 65535; empty means 8080."
      : undefined;
  const problem =
    (adv.healthPath && !adv.healthPath.startsWith("/")
      ? "The health check path starts with /, like /healthz."
      : undefined) ??
    (timeout === undefined || memory === undefined || !(cpus >= 0)
      ? "Timeout, CPUs and memory are positive numbers; leave them empty for no limit."
      : undefined);
  // What a submit still needs.
  const missing = [
    !name && "a name",
    source === "image" && !image.trim() && "an image",
    source === "github" && !repoValid && "a repository",
  ].filter((m) => typeof m === "string");
  const blocked =
    missing.length > 0 ||
    !!nameProblem(
      "service",
      name,
      services.map((s) => s.service.name),
    ) ||
    !!portError ||
    !!problem;
  // The footer sits beside three buttons, so after a failed submit it
  // counts the marked fields (each says what is wrong under it) instead
  // of naming them again on two lines.
  const marked = [nameError, imageError, repoError, portError].filter(
    Boolean,
  ).length;

  if (done) {
    return <Created env={env} done={done} onClose={onClose} />;
  }

  // The environment row, the project header's buttons, where there is a
  // choice.
  const pickEnvironment = choices.length > 1 && (
    <Group label="Environment">
      <EnvButtons
        environments={choices}
        picked={env.environment}
        onPick={(name) => {
          onEnvironment(name);
          create.reset();
        }}
      />
    </Group>
  );

  // The environment and source pickers, above a form of their own.
  const head = (
    <Fields>
      {pickEnvironment}
      <Group label="Source">
        <Choice
          label="Source"
          name="source"
          value={source}
          options={sources}
          onChange={setSource}
        />
      </Group>
    </Fields>
  );
  if (source === "template") {
    return (
      <FromTemplate
        env={env}
        taken={services.map((s) => s.service.name)}
        head={head}
        initial={template}
        onClose={onClose}
      />
    );
  }

  // Through the org's GitHub App, a repository and its directories are
  // picked from lists instead of typed (FromGitHub).
  const appRepos = repos.data?.repos ?? [];
  if (source === "github" && appRepos.length > 0) {
    return (
      <FromGitHub
        env={env}
        repos={appRepos}
        taken={services.map((s) => s.service.name)}
        onClose={onClose}
        head={head}
      />
    );
  }

  // The request: build settings only when built from source; empty
  // fields left out.
  const settings: Omit<Request, "name"> = {
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

  async function submit(deployIt: boolean) {
    if (!v.check(!blocked)) return;
    const service = await create.mutateAsync({ env, name, ...settings });
    if (source === "github") {
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
      // An image deploys straight away; the service page follows it.
      if (deployIt && source === "image") {
        await deploy.mutateAsync({ ...env, service: service.name });
      }
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
      ref={v.form}
      aria-label="New service"
      onSubmit={(e) => {
        e.preventDefault();
        // Failures show from the mutations' state.
        submit(true).catch(() => {});
      }}
    >
      <Fields>
        {pickEnvironment}
        <Field label="Name" error={nameError}>
          <input
            ref={autofocus}
            value={name}
            onChange={(e) => setTyped(toName(e.target.value))}
            placeholder="web"
            spellCheck={false}
            autoComplete="off"
            aria-invalid={!!nameError}
            className={`${input} ${invalid(nameError)}`}
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
            error={imageError}
          >
            <Text
              value={image}
              onChange={setImage}
              placeholder="ghcr.io/acme/api:1.4"
              error={imageError}
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
        {source === "github" && repos.isPending && (
          <span className="col-start-2 text-mut text-xs">
            Reading the repositories the GitHub App can see…
          </span>
        )}
        {source === "github" && !repos.isPending && (
          <>
            <span className="col-start-2 text-mut text-xs">
              {repos.error ? (
                <ErrorText error={repos.error} />
              ) : (
                <>
                  The GitHub App isn't installed for this organization. With it,
                  you pick a repository from a list and need no token:{" "}
                  <Link
                    to="/$org/settings/github"
                    params={{ org }}
                    onClick={onClose}
                  >
                    GitHub settings
                  </Link>
                  .
                </>
              )}
            </span>
            <Field label="Repository" error={repoError}>
              <Text
                value={repo}
                onChange={setRepo}
                placeholder="owner/repo"
                error={repoError}
              />
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
                autoComplete="new-password"
                className={input}
              />
            </Field>
          </>
        )}
        {
          <Field
            label="Port"
            note="Optional: the port the app listens on, 8080 if empty."
            error={portError}
          >
            <Text
              value={port}
              onChange={setPort}
              placeholder="8080"
              inputMode="numeric"
              width="w-24"
              error={portError}
            />
          </Field>
        }
      </Fields>

      {
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
      }

      {problem && <p className="mt-3 text-fail">{problem}</p>}
      <Footer
        submit={source === "image" ? "Create and deploy" : "Create service"}
        pending={create.isPending || connect.isPending || deploy.isPending}
        problem={
          v.tried && marked > 0 ? `Fix ${plural(marked, "field")}.` : undefined
        }
        error={create.error ?? deploy.error}
        cli={
          source === "image"
            ? [
                ...cli,
                `dock deploy --image ${image.trim() ? arg(image.trim()) : "<image>"} -s ${name || "<name>"} ${where}`,
              ]
            : cli
        }
        alt={
          source === "image" && (
            <Button
              disabled={create.isPending || deploy.isPending}
              onClick={() => submit(false).catch(() => {})}
            >
              Create, don't deploy
            </Button>
          )
        }
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
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  inputMode?: "numeric" | "decimal";
  width?: string;
  /** For fields that share a row and so a label. */
  label?: string;
  /** Marks the field as wrong; its Field says why. */
  error?: string;
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
      aria-invalid={!!error}
      className={`${input} ${width} ${invalid(error)}`}
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
  if (done.kind === "unconnected") {
    return (
      <div className="flex flex-col gap-3">
        <p>
          Created <span className="font-mono">{service.name}</span>, but
          connecting the repository failed. Fix it and connect again on its
          Settings tab.
        </p>
        <ErrorText error={done.error} />
        <div className="flex gap-2">
          <Link
            to="/$org/$project/$environment/$service/settings"
            params={{ org, ...ref }}
            className="btn pri"
            ref={autofocus}
            onClick={onClose}
          >
            Open Settings
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
      <span className="font-mono break-words">{text}</span>
      <CopyButton text={text} />
    </span>
  );
}
