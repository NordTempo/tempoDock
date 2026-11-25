import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, type ReactNode, useState } from "react";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";
import { keys, useSession } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, since } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  CopyButton,
  ErrorText,
} from "../../ui/kit";
import { autofocus, Choice, input } from "../create/form";
import {
  minutes,
  type PreviewData,
  previewDataText,
  previewQueries,
  type ServicePreviews,
  servicePreviewsCli,
  useSetServicePreviews,
} from "../previews/data";
import { canAdmin } from "../previews/PreviewsSection";
import { OpenValue } from "../settings/OpenValue";
import {
  commitText,
  type GitConnection,
  gitPath,
  useGitHubBranches,
  useGitHubPaths,
  useGitHubRepos,
  useGitSource,
} from "./api";
import { PickList, RepoPicker } from "./PickList";

type Schemas = components["schemas"];
type GitSource = Schemas["GitSource"];
type ConnectRequest = Schemas["GitConnectRequest"];
type ConnectResult = Schemas["GitConnectResult"];

/**
 * Settings' Source section (ADR 0018): where the service comes from, as
 * rows. Connected to GitHub, one "Deploys from" row (repository · branch
 * · path) whose editor changes each of them or disconnects; through
 * Dock's GitHub App (ADR 0031) they are picked from lists, through a
 * webhook they are typed. Then the image (only if one is set: the
 * repository is the source), the build, previews (ADR 0077) and commit
 * statuses. Unconnected, the image and a Repository row that connects
 * one. Changing the source needs deploy; previews need admin. Others see
 * it read-only. What pushes did is on History (LastPush), and deploying
 * a commit is in the header's Deploy.
 */
export function SourceSection({
  serviceRef: ref,
  image,
  imageSet,
  build,
}: {
  serviceRef: ServiceRef;
  /** The Image row, and whether an image is set (or staged). */
  image: ReactNode;
  imageSet: boolean;
  /** The Build row. */
  build: ReactNode;
}) {
  const client = useQueryClient();
  const session = useSession();
  const canEdit = session?.scope === "deploy" || session?.scope === "admin";
  const path = { params: { path: ref } };
  const source = useGitSource(ref);
  // The secret of a new or rotated webhook: shown until dismissed.
  const [created, setCreated] = useState<ConnectResult | null>(null);
  const [confirming, setConfirming] = useState<"disconnect" | "rotate" | null>(
    null,
  );

  const setConnection = (connection: GitConnection | undefined) =>
    client.setQueryData<GitSource>(
      keys.git(ref),
      (old) => old && { ...old, connection },
    );
  // The answer replaces the cached connection; git.* audit events
  // refetch the rest.
  const connect = useAction(
    async (req: ConnectRequest) => {
      const res = await body(api.PUT(gitPath, { ...path, body: req }));
      setConnection(res.connection);
      setConfirming(null);
      if (res.secret) setCreated(res);
      return res;
    },
    () => [],
  );
  const disconnect = useAction(
    async () => {
      await done(api.DELETE(gitPath, path));
      setConnection(undefined);
      setConfirming(null);
      setCreated(null);
    },
    // Previews follow the source.
    () => [keys.previews(ref.project)],
  );

  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const conn = source.data?.connection;
  const appReady = !!source.data?.app_ready;
  /** `dock git connect` for a target. */
  const connectCli = (
    repo: string,
    branch: string,
    dir: string,
    mode?: string,
  ) =>
    `dock git connect ${repo || "owner/repo"} --branch ${branch || "main"}${dir ? ` --dir ${dir}` : ""}${mode ? ` --mode ${mode}` : ""} ${flags}`;
  /** Saves one change of the connection, keeping the rest. */
  const save = (patch: Partial<ConnectRequest>, onDone: () => void) =>
    conn &&
    connect.mutate(
      { repo: conn.repo, branch: conn.branch, dir: conn.dir ?? "", ...patch },
      { onSuccess: onDone },
    );

  // Until the source is known, the image waits too, so it doesn't show
  // and then leave for a repository.
  const known = source.data !== undefined || source.error !== null;
  return (
    <div className="flex flex-col">
      {source.error && <ErrorText error={source.error} />}
      <dl className="props">
        {known && !conn && image}
        {source.data && !conn && (
          <Row
            key="connect"
            label="Repository"
            hint="Connect one and every push to its branch deploys."
            canEdit={canEdit}
            value={
              <span className="text-mut">
                none, it deploys what Deploy sends
              </span>
            }
            edit={(close) => (
              <ConnectForm
                appReady={appReady}
                pending={connect.isPending}
                error={connect.error}
                connectCli={connectCli}
                onSubmit={(req) => connect.mutate(req)}
                onCancel={close}
              />
            )}
          />
        )}
        {conn && (
          <Row
            key="source"
            label="Deploys from"
            canEdit={canEdit}
            value={
              <span className="flex flex-col gap-0.5">
                <span className="font-mono break-all">
                  {conn.repo} <span className="text-faint">·</span>{" "}
                  {conn.branch} <span className="text-faint">·</span>{" "}
                  {conn.dir ? `${conn.dir}/` : "/"}
                </span>
                <span className="text-xs text-mut">
                  every push to {conn.branch} deploys,{" "}
                  {conn.mode === "app"
                    ? "through the GitHub App"
                    : "through a webhook"}
                </span>
              </span>
            }
            edit={(close) => (
              <SourceEdit
                conn={conn}
                pending={connect.isPending}
                onDisconnect={() => {
                  close();
                  setConfirming("disconnect");
                }}
                editors={{
                  repository: (
                    <RepoEdit
                      conn={conn}
                      appReady={appReady}
                      pending={connect.isPending}
                      error={connect.error}
                      connectCli={connectCli}
                      onSave={(req) =>
                        connect.mutate(req, { onSuccess: close })
                      }
                      onCancel={close}
                    />
                  ),
                  branch:
                    conn.mode === "app" ? (
                      <BranchPick
                        repo={conn.repo}
                        current={conn.branch}
                        error={connect.error}
                        onPick={(branch) => save({ branch }, close)}
                        onCancel={close}
                        cli={(b) => connectCli(conn.repo, b, conn.dir ?? "")}
                      />
                    ) : (
                      <TextEdit
                        label="Branch"
                        initial={conn.branch}
                        placeholder="main"
                        error={connect.error}
                        pending={connect.isPending}
                        cli={(b) => connectCli(conn.repo, b, conn.dir ?? "")}
                        onSave={(branch) => save({ branch }, close)}
                        onCancel={close}
                      />
                    ),
                  path:
                    conn.mode === "app" ? (
                      <PathPick
                        repo={conn.repo}
                        branch={conn.branch}
                        current={conn.dir ?? ""}
                        error={connect.error}
                        onPick={(dir) => save({ dir }, close)}
                        onCancel={close}
                        cli={(d) => connectCli(conn.repo, conn.branch, d)}
                      />
                    ) : (
                      <TextEdit
                        label="Path"
                        initial={conn.dir ?? ""}
                        placeholder="the repository root"
                        error={connect.error}
                        pending={connect.isPending}
                        cli={(d) => connectCli(conn.repo, conn.branch, d)}
                        onSave={(dir) => save({ dir }, close)}
                        onCancel={close}
                      />
                    ),
                }}
              />
            )}
          />
        )}
        {conn && imageSet && image}
        {build}
        {source.data && (
          <PreviewsRow
            serviceRef={ref}
            branch={conn?.branch}
            canEdit={canAdmin(session, ref)}
          />
        )}
        {conn && (
          <>
            <Row
              label="Commit statuses"
              hint="Pending, success or failure on each commit, shown on GitHub."
              canEdit={canEdit}
              value={
                conn.status ? (
                  "posted to GitHub"
                ) : (
                  <span className="text-mut">not posted</span>
                )
              }
              edit={(close) =>
                conn.mode !== "app" && !conn.token_set ? (
                  <span className="flex flex-col">
                    <span className="text-mut">
                      Needs a token with Commit statuses write: set it on the
                      Webhook row.
                    </span>
                    <EditFoot error={null} onCancel={close} />
                  </span>
                ) : (
                  <form
                    aria-label="Edit commit statuses"
                    className="flex flex-col"
                    onSubmit={(e) => e.preventDefault()}
                    onKeyDown={(e) => e.key === "Escape" && close()}
                  >
                    <Choice
                      label="Commit statuses"
                      name="git-status"
                      value={conn.status ? "on" : "off"}
                      options={[
                        ["on", "posted"],
                        ["off", "not posted"],
                      ]}
                      onChange={(v) => save({ status: v === "on" }, close)}
                    />
                    <EditFoot error={connect.error} onCancel={close} />
                  </form>
                )
              }
            />
            {conn.mode !== "app" && (
              <Row
                label="Webhook"
                hint="GitHub sends pushes here."
                canEdit={canEdit}
                value={
                  <span className="flex flex-col gap-1">
                    {conn.webhook_url ? (
                      <span className="break-all font-mono">
                        {conn.webhook_url}
                      </span>
                    ) : (
                      <span className="text-warn">no public address yet</span>
                    )}
                    <span className="text-mut text-xs">
                      token {conn.token_set ? "set" : "not set"}
                    </span>
                  </span>
                }
                after={
                  conn.webhook_url && <CopyButton text={conn.webhook_url} />
                }
                edit={(close) => (
                  <TokenEdit
                    conn={conn}
                    pending={connect.isPending}
                    error={connect.error}
                    onSave={(token) => save({ token }, close)}
                    onRotate={() => {
                      close();
                      setConfirming("rotate");
                    }}
                    onCancel={close}
                  />
                )}
              />
            )}
            {source.data?.same_repo && source.data.same_repo.length > 0 && (
              <Row
                label="Same repository"
                hint="This environment's other services from it."
                value={
                  <Siblings serviceRef={ref} siblings={source.data.same_repo} />
                }
              />
            )}
          </>
        )}
      </dl>
      {conn && confirming === "rotate" && (
        <Confirm
          action="New secret"
          cli={`dock git connect ${conn.repo} --rotate-secret ${flags}`}
          pending={connect.isPending}
          error={connect.error}
          onConfirm={() =>
            connect.mutate({
              repo: conn.repo,
              branch: conn.branch,
              dir: conn.dir,
              rotate_secret: true,
            })
          }
          onCancel={() => setConfirming(null)}
        >
          Pushes are refused until the webhook on GitHub has the new secret.
        </Confirm>
      )}
      {conn && confirming === "disconnect" && (
        <Confirm
          danger
          action="Disconnect"
          cli={`dock git disconnect ${flags}`}
          pending={disconnect.isPending}
          error={disconnect.error}
          onConfirm={() => disconnect.mutate()}
          onCancel={() => setConfirming(null)}
        >
          Pushes stop deploying {ref.service}, and pull requests stop building
          it. Releases are kept.
          {conn.mode !== "app" &&
            " The token is deleted; delete the webhook on GitHub too."}
        </Confirm>
      )}
      {created?.secret && (
        <WebhookSetup result={created} onDone={() => setCreated(null)} />
      )}
      {source.data?.hooks_hint && (
        <p className="text-warn text-xs">{source.data.hooks_hint}</p>
      )}
    </div>
  );
}

/**
 * A settings row: the label, and the value, which opens `edit` (a small
 * form that calls close when done) with `hint` over it, for people who
 * can change it.
 */
function Row({
  label,
  hint,
  value,
  after,
  edit,
  canEdit,
}: {
  label: string;
  hint?: string;
  value: ReactNode;
  /** Links or buttons of its own beside the value (not inside its target). */
  after?: ReactNode;
  edit?: (close: () => void) => ReactNode;
  canEdit?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <dt>
        <span className="block text-sec">{label}</span>
      </dt>
      <dd>
        {open && edit ? (
          <span className="flex flex-col gap-1.5">
            {hint && <span className="text-xs text-mut">{hint}</span>}
            {edit(() => setOpen(false))}
          </span>
        ) : (
          <span className="flex items-start gap-3">
            <span className="min-w-0 flex-1">
              <OpenValue
                label={`Edit ${label.toLowerCase()}`}
                onOpen={edit && canEdit ? () => setOpen(true) : undefined}
              >
                {value}
              </OpenValue>
            </span>
            {after}
          </span>
        )}
      </dd>
    </>
  );
}

/** An edit form's last line: the error, Cancel, the named action. */
function EditFoot({
  error,
  cli,
  pending,
  submit,
  onCancel,
}: {
  error: Error | null;
  cli?: string;
  pending?: boolean;
  /** The submit button's word; none for pick lists, which save on ↵. */
  submit?: string;
  onCancel: () => void;
}) {
  return (
    <span className="mt-2 flex flex-wrap items-center gap-2">
      <span className="min-w-0 flex-1 text-xs">
        <ErrorText error={error} />
      </span>
      <Button small onClick={onCancel}>
        Cancel <kbd>Esc</kbd>
      </Button>
      {submit && (
        <Act small primary type="submit" disabled={pending} cli={cli}>
          {submit} <kbd>↵</kbd>
        </Act>
      )}
    </span>
  );
}

/** A connection's one typed value (webhook mode), saved with ↵. */
function TextEdit({
  label,
  initial,
  placeholder,
  error,
  pending,
  cli,
  onSave,
  onCancel,
}: {
  label: string;
  initial: string;
  placeholder: string;
  error: Error | null;
  pending: boolean;
  cli: (value: string) => string;
  onSave: (value: string) => void;
  onCancel: () => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <form
      aria-label={`Edit ${label.toLowerCase()}`}
      className="max-w-[760px]"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(value.trim());
      }}
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
    >
      <input
        aria-label={label}
        ref={autofocus}
        value={value}
        placeholder={placeholder}
        spellCheck={false}
        onChange={(e) => setValue(e.target.value)}
        className={`${input} w-full`}
      />
      <EditFoot
        error={error}
        cli={cli(value.trim())}
        pending={pending}
        submit="Save"
        onCancel={onCancel}
      />
    </form>
  );
}

/** Picks the branch through the App: the repository's branches. */
function BranchPick({
  repo,
  current,
  error,
  onPick,
  onCancel,
  cli,
}: {
  repo: string;
  current: string;
  error: Error | null;
  onPick: (branch: string) => void;
  onCancel: () => void;
  cli: (branch: string) => string;
}) {
  const branches = useGitHubBranches(repo);
  return (
    <div className="max-w-[760px]">
      {branches.error ? (
        <ErrorText error={branches.error} />
      ) : (
        <PickList
          items={branches.data?.branches ?? [current]}
          keyOf={(b) => b}
          textOf={(b) => b}
          label="Branches"
          onEscape={onCancel}
          loading={branches.isPending}
          placeholder={`Filter the branches of ${repo}, ↵ picks`}
          height="max-h-56"
          onPick={onPick}
          row={(b, on) => (
            <>
              <span
                className={`flex-1 font-mono ${on ? "text-fg" : "text-sec"}`}
              >
                {b}
              </span>
              {b === branches.data?.default_branch && (
                <span className="text-faint text-xs">default</span>
              )}
              {b === current && <span className="text-mut text-xs">now</span>}
            </>
          )}
        />
      )}
      <EditFoot error={error} cli={cli("<branch>")} onCancel={onCancel} />
    </div>
  );
}

/**
 * Picks the directory through the App: the ones that look like an app
 * first, with their marker files and the services building them; ↵ on
 * text that matches none uses it as typed.
 */
function PathPick({
  repo,
  branch,
  current,
  error,
  onPick,
  onCancel,
  cli,
}: {
  repo: string;
  branch: string;
  current: string;
  error: Error | null;
  onPick: (dir: string) => void;
  onCancel: () => void;
  cli: (dir: string) => string;
}) {
  const paths = useGitHubPaths(repo, branch);
  const items = [
    ...(paths.data?.roots ?? []).map((r) => ({
      path: r.path,
      markers: r.markers ?? [],
      used: r.used_by ?? [],
    })),
    ...(paths.data?.dirs ?? []).map((d) => ({
      path: d,
      markers: [] as string[],
      used: [] as string[],
    })),
  ];
  return (
    <div className="max-w-[760px]">
      {paths.error ? (
        <ErrorText error={paths.error} />
      ) : (
        <PickList
          items={items}
          keyOf={(p) => p.path}
          textOf={(p) => p.path}
          label="Directories"
          onEscape={onCancel}
          loading={paths.isPending}
          placeholder={`Filter the directories on ${branch}, ↵ picks`}
          initial={current}
          height="max-h-56"
          typed={onPick}
          onPick={(p) => onPick(p.path)}
          row={(p, on) => (
            <>
              <span
                className={`min-w-0 flex-1 truncate font-mono ${on ? "text-fg" : "text-sec"}`}
              >
                {p.path || "/"}
              </span>
              <span className="font-mono text-faint text-xs">
                {p.markers.join(" · ")}
              </span>
              <span className="text-mut text-xs">
                {p.used.map((u) => u.split("/").at(-1)).join(", ")}
              </span>
            </>
          )}
        />
      )}
      {paths.data?.truncated && (
        <span className="text-warn-text text-xs">
          The repository is too big to list whole; type a directory that isn't
          listed.
        </span>
      )}
      <EditFoot error={error} cli={cli("<dir>")} onCancel={onCancel} />
    </div>
  );
}

/**
 * The repository and how Dock reaches it: through the App (picked from
 * its repositories; another repository starts at its default branch and
 * root) or a webhook (typed, with a token for private ones).
 */
function RepoEdit({
  conn,
  appReady,
  pending,
  error,
  connectCli,
  onSave,
  onCancel,
}: {
  conn: GitConnection;
  appReady: boolean;
  pending: boolean;
  error: Error | null;
  connectCli: (
    repo: string,
    branch: string,
    dir: string,
    mode?: string,
  ) => string;
  onSave: (req: ConnectRequest) => void;
  onCancel: () => void;
}) {
  const [mode, setMode] = useState<"app" | "webhook">(conn.mode);
  const repos = useGitHubRepos(mode === "app");
  const [repo, setRepo] = useState(conn.repo);
  return (
    <div className="flex max-w-[760px] flex-col gap-2">
      {appReady && (
        <Choice
          label="Through"
          name="git-mode"
          value={mode}
          options={[
            ["app", "GitHub App"],
            ["webhook", "Webhook"],
          ]}
          onChange={setMode}
        />
      )}
      {mode === "app" ? (
        repos.error ? (
          <ErrorText error={repos.error} />
        ) : (
          <RepoPicker
            onEscape={onCancel}
            loading={repos.isPending}
            repos={repos.data?.repos ?? []}
            onPick={(r) =>
              onSave(
                r.full_name === conn.repo
                  ? {
                      mode: "app",
                      repo: conn.repo,
                      branch: conn.branch,
                      dir: conn.dir,
                    }
                  : {
                      mode: "app",
                      repo: r.full_name,
                      branch: r.default_branch,
                      dir: "",
                    },
              )
            }
          />
        )
      ) : (
        <form
          aria-label="Webhook repository"
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            onSave({
              mode: "webhook",
              repo: repo.trim(),
              branch: conn.branch,
              dir: conn.dir,
            });
          }}
        >
          <input
            aria-label="Repository"
            value={repo}
            placeholder="owner/repo"
            spellCheck={false}
            onChange={(e) => setRepo(e.target.value)}
            className={`${input} w-full`}
          />
          <EditFoot
            error={error}
            pending={pending}
            submit="Save"
            cli={connectCli(
              repo.trim(),
              conn.branch,
              conn.dir ?? "",
              appReady ? "webhook" : undefined,
            )}
            onCancel={onCancel}
          />
        </form>
      )}
      {mode === "app" && (
        <EditFoot
          error={error}
          cli={connectCli("<repo>", conn.branch, conn.dir ?? "")}
          onCancel={onCancel}
        />
      )}
    </div>
  );
}

/** A webhook connection's token: set, replace or remove; or a new secret. */
function TokenEdit({
  conn,
  pending,
  error,
  onSave,
  onRotate,
  onCancel,
}: {
  conn: GitConnection;
  pending: boolean;
  error: Error | null;
  onSave: (token: string) => void;
  onRotate: () => void;
  onCancel: () => void;
}) {
  const [token, setToken] = useState("");
  return (
    <form
      aria-label="Edit token"
      className="max-w-[760px]"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(token);
      }}
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
    >
      <input
        type="password"
        aria-label="GitHub token"
        autoComplete="new-password"
        ref={autofocus}
        value={token}
        placeholder={
          conn.token_set
            ? "a new token; empty removes it"
            : "fine-grained, Contents read"
        }
        onChange={(e) => setToken(e.target.value)}
        className={`${input} w-full`}
      />
      <span className="mt-1 block text-mut text-xs">
        Stored encrypted, never shown again.
      </span>
      <span className="mt-2 flex flex-wrap items-center gap-2">
        <Button small onClick={onRotate}>
          New webhook secret
        </Button>
        <span className="flex-1" />
      </span>
      <EditFoot
        error={error}
        pending={pending}
        submit={token ? "Save token" : conn.token_set ? "Remove token" : "Save"}
        cli={`dock git connect ${conn.repo} ${token ? "--token-stdin" : "--remove-token"}`}
        onCancel={onCancel}
      />
    </form>
  );
}

/** The environment's other services from the same repository. */
function Siblings({
  serviceRef: ref,
  siblings,
}: {
  serviceRef: ServiceRef;
  siblings: Schemas["GitSibling"][];
}) {
  const org = useOrg();
  return (
    <span className="flex flex-wrap gap-x-4 gap-y-1">
      {siblings.map((s) => (
        <span key={s.service}>
          <Link
            to="/$org/$project/$environment/$service/settings"
            params={{
              org,
              project: ref.project,
              environment: ref.environment,
              service: s.service,
            }}
            className="font-mono"
          >
            {s.service}
          </Link>{" "}
          <span className="font-mono text-faint">{s.dir || "/"}</span>
        </span>
      ))}
    </span>
  );
}

/** The parts of a repository source its editor changes, one at a time. */
type SourcePart = "repository" | "branch" | "path";

const sourceParts: readonly (readonly [SourcePart, string, string])[] = [
  ["repository", "Repository", "Where pushes come from."],
  ["branch", "Branch", "Every push to it deploys."],
  [
    "path",
    "Path",
    "What is built; pushes that change nothing in it are skipped.",
  ],
];

/**
 * The Deploys from row's editor: pick the repository, branch or path and
 * change it with its own editor (branch first, the usual change); under
 * them who connected it, a link to it on GitHub and Disconnect. While a
 * save is `pending` the picks are disabled: each save sends the whole
 * connection from the cache, so a second one would undo the first.
 */
function SourceEdit({
  conn,
  pending,
  editors,
  onDisconnect,
}: {
  conn: GitConnection;
  pending: boolean;
  editors: Record<SourcePart, ReactNode>;
  onDisconnect: () => void;
}) {
  const [part, setPart] = useState<SourcePart>("branch");
  const hint = sourceParts.find(([p]) => p === part)?.[2];
  return (
    <span className="flex flex-col gap-2">
      <fieldset disabled={pending} className="flex min-w-0 flex-col gap-2">
        <Choice
          label="Change"
          name="git-source-part"
          value={part}
          options={sourceParts.map(([p, label]) => [p, label] as const)}
          onChange={setPart}
        />
        <span className="text-xs text-mut">{hint}</span>
        <span key={part}>{editors[part]}</span>
      </fieldset>
      <span className="flex flex-wrap items-center gap-x-4 gap-y-2 border-soft border-t pt-2 text-xs text-mut">
        <span>
          connected {ago(conn.created_at)} by {conn.created_by}
        </span>
        <a
          href={`https://github.com/${conn.repo}`}
          target="_blank"
          rel="noreferrer"
        >
          Open on GitHub ↗
        </a>
        <span className="flex-1" />
        <Button small danger onClick={onDisconnect}>
          Disconnect…
        </Button>
      </span>
    </span>
  );
}

/**
 * What GitHub's last push did (deployed, ignored and why) and a commit
 * queued behind the deploy in flight, for a service connected to a
 * repository: a line on History, since it is a record, not a setting.
 * Nothing until a push arrives.
 */
export function LastPush({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const conn = useGitSource(ref).data?.connection;
  if (!conn || (!conn.last_event && !conn.pending)) return null;
  return (
    <p className="flex flex-wrap gap-x-3 text-mut">
      {conn.last_event && (
        <span>
          Last push from GitHub:{" "}
          <span className="text-sec">{conn.last_event}</span>
          {conn.last_event_at && (
            <span className="text-faint"> · {since(conn.last_event_at)}</span>
          )}
        </span>
      )}
      {conn.pending && (
        <span>
          queued {commitText(conn.pending)}, waits for the current deploy
        </span>
      )}
    </p>
  );
}

/**
 * What the service does in previews (ADR 0077): whether pull requests
 * into its branch build it, forks too, the literal values copied, and
 * whether its volumes start from its newest snapshot. Its environment's
 * numbers are on the project's Previews page.
 */
function PreviewsRow({
  serviceRef: ref,
  branch,
  canEdit,
}: {
  serviceRef: ServiceRef;
  branch: string | undefined;
  canEdit: boolean;
}) {
  const sp = useQuery(previewQueries.service(ref));
  const d = sp.data;
  if (sp.error) {
    return <Row label="Previews" value={<ErrorText error={sp.error} />} />;
  }
  if (!d) return null;
  const parts: ReactNode[] = [];
  if (d.build) parts.push(<span key="b">on</span>);
  else if (branch)
    parts.push(
      <span key="b" className="text-mut">
        off
      </span>,
    );
  if (d.build)
    parts.push(
      <span key="f" className="text-mut">
        {d.allow_forks ? "forks too" : "forks off"}
      </span>,
    );
  if (d.copy_variables.length > 0)
    parts.push(
      <span key="c">
        <span className="text-mut">copies </span>
        <span className="font-mono">{d.copy_variables.join(", ")}</span>
      </span>,
    );
  if (d.data !== "empty")
    parts.push(
      <span key="s">
        {previewDataText(d.data)} of {ref.environment}/{ref.service}
      </span>,
    );
  if (d.data === "masked" && d.masked?.columns)
    parts.push(
      <span key="m" className="text-mut">
        {d.masked.columns} columns masked
      </span>,
    );
  if (d.data !== "empty")
    parts.push(
      <span key="df" className="text-mut">
        {d.data_forks ? "forks get the data" : "forks start empty"}
      </span>,
    );
  if (parts.length === 0)
    parts.push(
      <span key="n" className="text-mut">
        copies nothing, volumes start empty
      </span>,
    );
  return (
    <Row
      label="Previews"
      hint={
        branch
          ? `Pull requests into ${branch}.`
          : "In other services' previews."
      }
      canEdit={canEdit}
      value={
        <span className="flex flex-wrap gap-x-1.5">
          {parts.map((p, i) => (
            <span key={(p as { key?: string }).key ?? i}>
              {i > 0 && <span className="text-faint">· </span>}
              {p}
            </span>
          ))}
        </span>
      }
      edit={(close) => (
        <PreviewsEdit
          serviceRef={ref}
          branch={branch}
          current={d}
          onDone={close}
        />
      )}
    />
  );
}

/** Edits a service's previews (`dock service previews`). */
function PreviewsEdit({
  serviceRef: ref,
  branch,
  current,
  onDone,
}: {
  serviceRef: ServiceRef;
  branch: string | undefined;
  current: ServicePreviews;
  onDone: () => void;
}) {
  const org = useOrg();
  const set = useSetServicePreviews();
  const env = useQuery(previewQueries.environment(ref));
  const [build, setBuild] = useState(current.build);
  const [forks, setForks] = useState(current.allow_forks);
  const [data, setData] = useState<PreviewData>(current.data);
  const [dataForks, setDataForks] = useState(current.data_forks);
  const [copies, setCopies] = useState(current.copy_variables.join(", "));
  const keys = copies.split(/[\s,]+/).filter(Boolean);
  const cli = servicePreviewsCli(
    `${build ? "on" : "off"}${build && forks ? " --forks" : ""}${keys.map((k) => ` --copy ${k}`).join("")}${data !== current.data ? ` --data ${data}` : ""}${data !== "empty" && dataForks ? " --data-forks" : ""}`,
    ref,
  );
  return (
    <form
      aria-label="Edit previews"
      className="flex max-w-[760px] flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        set.mutate(
          {
            ref,
            req: {
              build,
              allow_forks: build && forks,
              copy_variables: keys,
              data,
              data_forks: data !== "empty" && dataForks,
            },
          },
          { onSuccess: onDone },
        );
      }}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
    >
      <Check
        label={
          branch ? (
            <>
              Build a preview for pull requests into{" "}
              <span className="font-mono">{branch}</span>
            </>
          ) : (
            "Build pull requests"
          )
        }
        checked={build}
        disabled={!branch}
        reason="Connect a repository first"
        onChange={setBuild}
      />
      <Check
        label="Also pull requests from forks: they run code from anyone on GitHub"
        checked={build && forks}
        disabled={!build}
        onChange={setForks}
      />
      <PreviewDataChoice
        serviceRef={ref}
        current={current}
        value={data}
        onChange={setData}
      />
      <Check
        label="Forks get the data too; otherwise their previews start empty"
        checked={data !== "empty" && dataForks}
        disabled={data === "empty"}
        onChange={setDataForks}
      />
      <label className="flex flex-col gap-1">
        <span className="text-mut text-xs">
          Variables whose literal values previews copy; no other value is copied
        </span>
        <input
          value={copies}
          placeholder="none, e.g. PUBLIC_API_URL"
          spellCheck={false}
          onChange={(e) => setCopies(e.target.value)}
          className={`${input} w-full`}
        />
      </label>
      {env.data && (
        <span className="text-mut text-xs">
          {ref.environment}'s previews: at most {env.data.max_previews} open,{" "}
          {env.data.sleep_after_minutes > 0
            ? `sleep after ${minutes(env.data.sleep_after_minutes)}`
            : "never sleep"}{" "}
          ·{" "}
          <Link
            to="/$org/$project/project-settings"
            params={{ org, project: ref.project }}
            hash="previews"
          >
            change
          </Link>
        </span>
      )}
      <EditFoot
        error={set.error}
        pending={set.isPending}
        submit="Save"
        cli={cli}
        onCancel={onDone}
      />
    </form>
  );
}

/**
 * Picks what a service's volumes start with in a new preview (ADR 0092),
 * with what that means under it: copies are for template databases (the
 * API names their sensitive marks), and real data turns the line into a
 * warning.
 */
function PreviewDataChoice({
  serviceRef: ref,
  current,
  value,
  onChange,
}: {
  serviceRef: ServiceRef;
  current: ServicePreviews;
  value: PreviewData;
  onChange: (v: PreviewData) => void;
}) {
  const org = useOrg();
  const db = current.masked;
  const options: (readonly [PreviewData, string])[] = [
    ["empty", "Empty"],
    ["snapshot", "Snapshot"],
  ];
  if (db) options.push(["masked", "Masked copy"], ["copy", "Copy"]);
  const base = `${ref.environment}/${ref.service}`;
  const toData = (
    <Link
      to="/$org/$project/$environment/$service/data"
      params={{ org, ...ref }}
      className="text-info hover:underline"
    >
      {db?.columns ? "Mark more in Data" : "Mark them in Data"}
    </Link>
  );
  let line: ReactNode = "Its volumes start empty.";
  if (value === "snapshot")
    line = (
      <span className="text-warn">
        ▲ Its newest snapshot, as it is: real data, sensitive columns included.
      </span>
    );
  if (value === "copy")
    line = (
      <span className="text-warn">
        ▲ Real data, sensitive columns included. Code in any pull request runs
        against it before anyone reviews it.
      </span>
    );
  if (value === "masked" && db)
    line =
      db.keys && db.keys > 0 ? (
        <span className="text-warn">
          {base} has {db.keys} sensitive key patterns, which a masked copy can't
          leave out yet: previews start empty.
        </span>
      ) : db.columns > 0 ? (
        <>
          A fresh copy of {base} when a preview opens, with {db.columns}{" "}
          sensitive columns in {db.tables} tables replaced as it is read.{" "}
          {toData}
        </>
      ) : (
        <>No sensitive columns are marked, so nothing is masked. {toData}</>
      );
  return (
    <div className="flex flex-col gap-1">
      <span className="text-mut text-xs">Data in previews</span>
      <Choice
        label="Data in previews"
        name="preview-data"
        value={value}
        options={options}
        onChange={onChange}
      />
      <span className="text-xs">{line}</span>
      {(value === "copy" || value === "masked") && current.copy_max_mb && (
        <span className="text-mut text-xs">
          Up to{" "}
          {current.copy_max_mb >= 1024
            ? `${current.copy_max_mb / 1024} GB`
            : `${current.copy_max_mb} MB`}
          ; a larger one starts empty · previews on {ref.environment}'s server
          only
        </span>
      )}
    </div>
  );
}

/**
 * Connects a repository: through the App, picked from the org's
 * repositories, then the branch and directory; else typed, with a token
 * for a private repository.
 */
function ConnectForm({
  appReady,
  pending,
  error,
  connectCli,
  onSubmit,
  onCancel,
}: {
  appReady: boolean;
  pending: boolean;
  error: Error | null;
  connectCli: (
    repo: string,
    branch: string,
    dir: string,
    mode?: string,
  ) => string;
  onSubmit: (req: ConnectRequest) => void;
  onCancel: () => void;
}) {
  const [mode, setMode] = useState<"app" | "webhook">(
    appReady ? "app" : "webhook",
  );
  const repos = useGitHubRepos(mode === "app");
  const [repo, setRepo] = useState<Schemas["GitHubRepo"] | null>(null);
  const [branch, setBranch] = useState("");
  const [typedRepo, setTypedRepo] = useState("");
  const [dir, setDir] = useState("");
  const [token, setToken] = useState("");
  return (
    <div className="flex max-w-[760px] flex-col gap-2.5">
      {appReady && (
        <Choice
          label="Through"
          name="git-connect-mode"
          value={mode}
          options={[
            ["app", "GitHub App"],
            ["webhook", "Webhook"],
          ]}
          onChange={setMode}
        />
      )}
      {mode === "app" && !repo && (
        <>
          {repos.error ? (
            <ErrorText error={repos.error} />
          ) : (
            <RepoPicker
              onEscape={onCancel}
              loading={repos.isPending}
              repos={repos.data?.repos ?? []}
              onPick={(r) => {
                setRepo(r);
                setBranch(r.default_branch);
              }}
            />
          )}
          <EditFoot error={error} onCancel={onCancel} />
        </>
      )}
      {mode === "app" && repo && (
        <>
          <span className="flex items-center gap-2">
            <span className="flex-1 font-mono text-fg">{repo.full_name}</span>
            <Button small onClick={() => setRepo(null)}>
              Another repository
            </Button>
          </span>
          <PathPick
            repo={repo.full_name}
            branch={branch}
            current=""
            error={error}
            onPick={(d) =>
              onSubmit({ mode: "app", repo: repo.full_name, branch, dir: d })
            }
            onCancel={onCancel}
            cli={(d) => connectCli(repo.full_name, branch, d)}
          />
          <span className="text-mut text-xs">
            Pick the directory to build at{" "}
            <span className="font-mono">{branch}</span>; ↵ connects. Change the
            branch after connecting.
          </span>
          {pending && <span className="text-mut text-xs">Connecting…</span>}
        </>
      )}
      {mode === "webhook" && (
        <form
          aria-label="Connect through a webhook"
          className="grid grid-cols-[100px_minmax(0,1fr)] items-center gap-x-3 gap-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit({
              mode: appReady ? "webhook" : undefined,
              repo: typedRepo.trim(),
              branch: branch.trim() || "main",
              dir: dir.trim(),
              ...(token ? { token } : {}),
            });
          }}
        >
          <span className="text-mut text-xs">Repository</span>
          <input
            aria-label="Repository"
            ref={autofocus}
            value={typedRepo}
            placeholder="owner/repo"
            spellCheck={false}
            onChange={(e) => setTypedRepo(e.target.value)}
            className={`${input} w-full`}
          />
          <span className="text-mut text-xs">Branch</span>
          <input
            aria-label="Branch"
            value={branch}
            placeholder="main"
            spellCheck={false}
            onChange={(e) => setBranch(e.target.value)}
            className={`${input} w-full`}
          />
          <span className="text-mut text-xs">Path</span>
          <input
            aria-label="Path"
            value={dir}
            placeholder="the repository root"
            spellCheck={false}
            onChange={(e) => setDir(e.target.value)}
            className={`${input} w-full`}
          />
          <span className="text-mut text-xs">Token</span>
          <input
            type="password"
            aria-label="GitHub token"
            autoComplete="new-password"
            value={token}
            placeholder="for a private repository: fine-grained, Contents read"
            onChange={(e) => setToken(e.target.value)}
            className={`${input} w-full`}
          />
          <span className="col-span-2">
            <EditFoot
              error={error}
              pending={pending}
              submit="Connect"
              cli={connectCli(
                typedRepo.trim(),
                branch.trim(),
                dir.trim(),
                appReady ? "webhook" : undefined,
              )}
              onCancel={onCancel}
            />
          </span>
        </form>
      )}
    </div>
  );
}

/** The webhook to add on GitHub, with its secret: shown once. */
function WebhookSetup({
  result,
  onDone,
}: {
  result: ConnectResult;
  onDone: () => void;
}) {
  const conn = result.connection;
  const secret = result.secret ?? "";
  return (
    <div
      role="alert"
      className="mt-2 flex max-w-[860px] flex-col gap-2 rounded-panel bg-panel p-3"
    >
      <span>
        On GitHub, open{" "}
        <a
          href={`https://github.com/${conn.repo}/settings/hooks/new`}
          target="_blank"
          rel="noreferrer"
        >
          {conn.repo} › Settings › Webhooks › Add webhook
        </a>{" "}
        and set:
      </span>
      <dl className="props">
        <dt>Payload URL</dt>
        <dd>
          {conn.webhook_url ? (
            <span className="inline-flex flex-wrap items-baseline gap-2">
              <span className="font-mono">{conn.webhook_url}</span>
              <CopyButton text={conn.webhook_url} />
            </span>
          ) : (
            <span className="text-warn">
              none yet: set Dock's public address first
            </span>
          )}
        </dd>
        <dt>Content type</dt>
        <dd className="font-mono">application/json</dd>
        <dt>Secret</dt>
        <dd>
          <span className="inline-flex flex-wrap items-baseline gap-2">
            <span className="break-all font-mono">{secret}</span>
            <CopyButton text={secret} />
          </span>
        </dd>
        <dt>Events</dt>
        <dd>Pushes and pull requests</dd>
      </dl>
      <span className="flex items-center gap-3">
        <span className="text-warn">The secret is not shown again.</span>
        <Button small onClick={onDone}>
          Done
        </Button>
      </span>
    </div>
  );
}
