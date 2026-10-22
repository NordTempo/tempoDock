import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { keys, useSession } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { since } from "../../ui/format";
import {
  Button,
  Check,
  Confirm,
  CopyButton,
  ErrorText,
  SectionHead,
} from "../../ui/kit";

type Schemas = components["schemas"];
type GitSource = Schemas["GitSource"];
type GitConnection = Schemas["GitConnection"];
type ConnectRequest = Schemas["GitConnectRequest"];
type ConnectResult = Schemas["GitConnectResult"];

const gitPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/git";

const shortSha = (sha: string) => sha.slice(0, 7);

/** "acme/shop@main in apps/web/". */
const targetText = (c: GitConnection) =>
  `${c.repo}@${c.branch}${c.dir ? ` in ${c.dir}/` : ""}`;

/**
 * The service's GitHub source (ADR 0018): pushes to a branch deploy it
 * from source. Connecting shows the webhook secret once. Changing it needs
 * scope deploy; others see it read-only.
 */
export function SourceSection({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const client = useQueryClient();
  const scope = useSession()?.scope;
  const canEdit = scope === "deploy" || scope === "admin";
  const path = { params: { path: ref } };
  const source = useQuery({
    queryKey: keys.git(ref),
    queryFn: () => body(api.GET(gitPath, path)),
  });
  // The secret of a new or rotated webhook: shown until dismissed.
  const [created, setCreated] = useState<ConnectResult | null>(null);
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const setConnection = (connection: GitConnection | undefined) =>
    client.setQueryData<GitSource>(
      keys.git(ref),
      (old) => old && { ...old, connection },
    );
  const connect = useMutation({
    mutationFn: (req: ConnectRequest) =>
      body(api.PUT(gitPath, { ...path, body: req })),
    onSuccess: (res) => {
      setConnection(res.connection);
      setEditing(false);
      if (res.secret) setCreated(res);
    },
  });
  const deploy = useMutation({
    mutationFn: () =>
      body(api.POST(`${gitPath}/deploy`, { ...path, body: {} })),
  });
  const disconnect = useMutation({
    mutationFn: () => done(api.DELETE(gitPath, path)),
    onSuccess: () => {
      setConnection(undefined);
      setConfirming(false);
      setCreated(null);
    },
  });

  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const conn = source.data?.connection;
  return (
    <section aria-labelledby="source" className="border-t border-line pt-4">
      <SectionHead id="source" aside="deploy on every push to a GitHub branch">
        Source
      </SectionHead>
      {source.error ? (
        <ErrorText error={source.error} />
      ) : !source.data ? null : !conn || editing ? (
        canEdit ? (
          <ConnectForm
            connection={conn}
            pending={connect.isPending}
            error={connect.error}
            onSubmit={(req) => connect.mutate(req)}
            onCancel={conn ? () => setEditing(false) : undefined}
            cli={`dock git connect owner/repo --branch main ${flags}`}
          />
        ) : (
          <p className="text-mut">Not connected to GitHub.</p>
        )
      ) : (
        <>
          <dl className="grid max-w-[860px] grid-cols-[180px_minmax(0,1fr)] gap-x-4 gap-y-2">
            <Item label="Repository">
              <a
                href={`https://github.com/${conn.repo}/tree/${conn.branch}${conn.dir ? `/${conn.dir}` : ""}`}
                target="_blank"
                rel="noreferrer"
                className="font-mono"
              >
                {targetText(conn)}
              </a>
            </Item>
            <Item label="Webhook">
              {conn.webhook_url ? (
                <span className="inline-flex flex-wrap items-baseline gap-2">
                  <span className="font-mono">{conn.webhook_url}</span>
                  <CopyButton text={conn.webhook_url} />
                </span>
              ) : (
                <span className="text-warn">no public address yet</span>
              )}
            </Item>
            <Item label="Token">
              {conn.token_set ? "set" : "not set"}
              <span className="text-mut">
                {" "}
                · statuses {conn.status ? "on" : "off"}
              </span>
            </Item>
            {conn.pending && (
              <Item label="Queued">
                <span className="font-mono">{shortSha(conn.pending.sha)}</span>{" "}
                {conn.pending.message}{" "}
                <span className="text-mut">waits for the current deploy</span>
              </Item>
            )}
            <Item label="Last push">
              {conn.last_event ? (
                <>
                  {conn.last_event}
                  {conn.last_event_at && (
                    <span className="text-mut">
                      {" "}
                      · {since(conn.last_event_at)}
                    </span>
                  )}
                </>
              ) : (
                <span className="text-mut">nothing received yet</span>
              )}
            </Item>
          </dl>
          {canEdit && !confirming && (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                small
                primary
                disabled={deploy.isPending}
                title={`dock git deploy ${flags}`}
                onClick={() => deploy.mutate()}
              >
                Deploy {conn.branch} now
              </Button>
              <Button small onClick={() => setEditing(true)}>
                Edit
              </Button>
              <Button
                small
                disabled={connect.isPending}
                onClick={() =>
                  connect.mutate({
                    repo: conn.repo,
                    branch: conn.branch,
                    dir: conn.dir,
                    rotate_secret: true,
                  })
                }
              >
                New secret
              </Button>
              <Button small danger onClick={() => setConfirming(true)}>
                Disconnect
              </Button>
              {deploy.data && (
                <span className="text-mut">
                  queued {shortSha(deploy.data.commit.sha)}{" "}
                  {deploy.data.commit.message}
                </span>
              )}
              <ErrorText error={deploy.error ?? connect.error} />
            </div>
          )}
          {confirming && (
            <Confirm
              danger
              action="Disconnect"
              cli={`dock git disconnect ${flags}`}
              pending={disconnect.isPending}
              error={disconnect.error}
              onConfirm={() => disconnect.mutate()}
              onCancel={() => setConfirming(false)}
            >
              Pushes stop deploying {ref.service} and the token is deleted.
              Releases are kept. Delete the webhook on GitHub too.
            </Confirm>
          )}
        </>
      )}
      {created?.secret && (
        <WebhookSetup result={created} onDone={() => setCreated(null)} />
      )}
      {source.data?.hooks_hint && (
        <p className="text-xs text-warn">{source.data.hooks_hint}</p>
      )}
    </section>
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

/** Connects a repository, or edits the connection (token blank: keep). */
function ConnectForm({
  connection: conn,
  pending,
  error,
  onSubmit,
  onCancel,
  cli,
}: {
  connection: GitConnection | undefined;
  pending: boolean;
  error: Error | null;
  onSubmit: (req: ConnectRequest) => void;
  onCancel?: () => void;
  cli: string;
}) {
  const [repo, setRepo] = useState(conn?.repo ?? "");
  const [branch, setBranch] = useState(conn?.branch ?? "main");
  const [dir, setDir] = useState(conn?.dir ?? "");
  const [token, setToken] = useState("");
  const [removeToken, setRemoveToken] = useState(false);
  const [status, setStatus] = useState(conn?.status ?? false);
  const hasToken = (conn?.token_set && !removeToken) || token !== "";

  const submit = () =>
    onSubmit({
      repo: repo.trim(),
      branch: branch.trim(),
      dir: dir.trim(),
      status: status && hasToken,
      ...(token ? { token } : removeToken ? { token: "" } : {}),
    });

  const input = (
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder: string,
    width = "w-56",
  ) => (
    <input
      aria-label={label}
      value={value}
      placeholder={placeholder}
      onChange={(e) => set(e.target.value)}
      onKeyDown={(e) => e.key === "Enter" && submit()}
      className={`field ${width} py-0.5 font-mono text-xs`}
    />
  );

  return (
    <div className="flex max-w-[860px] flex-col gap-2">
      <dl className="grid grid-cols-[180px_minmax(0,1fr)] gap-x-4 gap-y-2">
        <Item label="Repository">
          {input("Repository", repo, setRepo, "owner/repo")}
        </Item>
        <Item label="Branch">{input("Branch", branch, setBranch, "main")}</Item>
        <Item label="Directory">
          {input("Directory", dir, setDir, "repository root")}{" "}
          <span className="text-mut">build context, for monorepos</span>
        </Item>
        <Item label="Token">
          <span className="inline-flex flex-wrap items-baseline gap-3">
            <input
              type="password"
              aria-label="GitHub token"
              autoComplete="off"
              value={token}
              placeholder={conn?.token_set ? "unchanged" : "public repository"}
              onChange={(e) => setToken(e.target.value)}
              className="field w-56 py-0.5 font-mono text-xs"
            />
            {conn?.token_set && !token && (
              <Check
                label="remove"
                checked={removeToken}
                onChange={setRemoveToken}
              />
            )}
            <span className="text-mut">
              fine-grained, Contents read; stored encrypted
            </span>
          </span>
        </Item>
        <Item label="Statuses">
          <Check
            label="post commit statuses to GitHub"
            checked={status && hasToken}
            disabled={!hasToken}
            title={
              hasToken ? undefined : "Needs a token with Commit statuses write"
            }
            onChange={setStatus}
          />
        </Item>
      </dl>
      <div className="flex flex-wrap items-center gap-2">
        <Button small primary disabled={pending || !repo} onClick={submit}>
          {conn ? "Save" : "Connect"}
        </Button>
        {onCancel && (
          <Button small onClick={onCancel}>
            Cancel
          </Button>
        )}
        <code className="font-mono text-2xs text-mut">{cli}</code>
        <ErrorText error={error} />
      </div>
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
      className="mt-2 flex max-w-[860px] flex-col gap-2 border border-edge p-3"
    >
      <span>
        On GitHub, open{" "}
        <a
          href={`https://github.com/${conn.repo}/settings/hooks/new`}
          target="_blank"
          rel="noreferrer"
        >
          {conn.repo} → Settings → Webhooks → Add webhook
        </a>{" "}
        and set:
      </span>
      <dl className="grid grid-cols-[180px_minmax(0,1fr)] gap-x-4 gap-y-1">
        <Item label="Payload URL">
          {conn.webhook_url ? (
            <span className="inline-flex flex-wrap items-baseline gap-2">
              <span className="font-mono">{conn.webhook_url}</span>
              <CopyButton text={conn.webhook_url} />
            </span>
          ) : (
            <span className="text-warn">
              none yet: set [hooks] public_host first
            </span>
          )}
        </Item>
        <Item label="Content type">
          <span className="font-mono">application/json</span>
        </Item>
        <Item label="Secret">
          <span className="inline-flex flex-wrap items-baseline gap-2">
            <span className="font-mono break-all">{secret}</span>
            <CopyButton text={secret} />
          </span>
        </Item>
        <Item label="Events">Just the push event</Item>
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
