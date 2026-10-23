import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { useCreateToken, useRevokeToken } from "../data/mutations";
import {
  queries,
  useIsAdmin,
  useProjects,
  useProjectTree,
  useTokens,
} from "../data/queries";
import type { Scope, Token } from "../data/types";
import { useSaveTokenData } from "../features/data/dockPolicy";
import {
  TokenDataEditor,
  TokenDataFields,
  tokenDataFlags,
  tokenDataText,
} from "../features/data/TokenData";
import type { DataTokenAccess } from "../features/data/types";
import { ago, isoDay, plural } from "../ui/format";
import {
  Act,
  Actor,
  actorKind,
  Button,
  Check,
  CopyButton,
  ErrorText,
  Page,
  Panel,
  SectionHead,
  TitleRow,
} from "../ui/kit";

export const Route = createFileRoute("/_app/tokens")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.tokens()),
  component: Tokens,
});

function Tokens() {
  const tokens = useTokens();
  const live = tokens.filter((t) => !t.revoked_at);
  const [created, setCreated] = useState<{
    name: string;
    secret: string;
    project?: string;
  }>();

  return (
    <Page>
      <TitleRow
        title="Tokens"
        context="For agents, CI and scripts. Every action a token takes shows in the audit log under its name."
      />
      {created && (
        <section
          aria-labelledby="new-token"
          className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
        >
          <h2 id="new-token" className="font-semibold">
            <span className="text-ok">✓</span> Created{" "}
            <span className="font-mono font-medium">{created.name}</span>
          </h2>
          <p>
            <b className="font-semibold">Copy it now.</b>{" "}
            <span className="text-mut">
              This is the only time the secret is shown. If you lose it, create
              a new token.
            </span>
          </p>
          <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
            <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
              {created.secret}
            </code>
            <CopyButton text={created.secret} primary />
          </div>
          <p className="font-mono text-xs text-mut">
            export DOCK_TOKEN=… then: dock deploy
            {created.project ? ` -p ${created.project}` : ""}
          </p>
          <McpConfig secret={created.secret} />
          <div>
            <Button small onClick={() => setCreated(undefined)}>
              I've saved it
            </Button>
          </div>
        </section>
      )}

      <section aria-labelledby="tokens">
        <SectionHead id="tokens">{plural(live.length, "token")}</SectionHead>
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Scope</th>
                <th>Limited to</th>
                <th>Created by</th>
                <th>Data</th>
                <th>Last used</th>
                <th>Expires</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tokens.map((token) => (
                <TokenRow key={token.id} token={token} />
              ))}
            </tbody>
          </table>
        </Panel>
        {tokens.length === 0 && <p className="py-2 text-mut">No tokens yet.</p>}
      </section>

      <CreateToken onCreated={setCreated} />
      {!created && (
        <section
          aria-labelledby="mcp"
          className="flex max-w-[760px] flex-col gap-2.5"
        >
          <SectionHead id="mcp">Connect an agent</SectionHead>
          <McpConfig />
        </section>
      )}
    </Page>
  );
}

/**
 * What `dock mcp` prints: the MCP endpoint and how Claude Code or another
 * MCP client reaches it with a token. Without `secret` the token is
 * ${DOCK_TOKEN}, which Claude Code expands, so the config can be
 * committed; with it (a token just created) it can be written in, like
 * `dock mcp --show-token`.
 */
function McpConfig({ secret }: { secret?: string }) {
  const [name, setName] = useState("dock");
  const [inline, setInline] = useState(false);
  const url = `${window.location.origin}/api/v1/mcp`;
  // biome-ignore lint/suspicious/noTemplateCurlyInString: a literal placeholder, Claude Code expands it from the environment
  const token = secret && inline ? secret : "${DOCK_TOKEN}";
  const header = `Authorization: Bearer ${token}`;
  const server = name.trim() || "dock";
  const claude = `claude mcp add --transport http --scope project ${server} ${url} --header '${header}'`;
  const json = JSON.stringify(
    {
      mcpServers: {
        [server]: {
          type: "http",
          url,
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  );
  const block = (label: string, text: string) => (
    <div className="flex flex-col gap-1">
      <span className="flex items-center gap-2 text-xs text-mut">
        {label} <CopyButton text={text} />
      </span>
      <pre className="overflow-x-auto rounded-ctl border border-line bg-side px-3 py-2 font-mono text-xs">
        {text}
      </pre>
    </div>
  );
  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-2">
          <span className="text-mut">Name in the config</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="field w-32 py-0.5 font-mono text-xs"
          />
        </label>
        {secret && (
          <Check
            label="write this token in (don't commit it)"
            checked={inline}
            onChange={setInline}
          />
        )}
        <span className="font-mono text-2xs text-faint">
          dock mcp
          {name.trim() && name.trim() !== "dock" ? ` --name ${server}` : ""}
          {secret && inline ? " --show-token" : ""}
        </span>
      </div>
      {block("Claude Code, writes .mcp.json", claude)}
      {block(".mcp.json, or another client's mcp.json", json)}
      {!(secret && inline) && (
        <p className="text-xs text-mut">
          Set <span className="font-mono">DOCK_TOKEN</span> to a scoped token
          for the agent, never your own login. The agent gets the token's
          permissions.
        </p>
      )}
    </div>
  );
}

function TokenRow({ token }: { token: Token }) {
  const revoke = useRevokeToken();
  const admin = useIsAdmin();
  const [confirming, setConfirming] = useState(false);
  const [editingData, setEditingData] = useState(false);
  const saveData = useSaveTokenData();
  const expired =
    !!token.expires_at && Date.parse(token.expires_at) < Date.now();
  const dead = !!token.revoked_at || expired;
  return (
    <>
      <tr className={dead ? "text-faint" : undefined}>
        <td className={`py-2.5 ${dead ? "" : "text-info"}`}>◇ {token.name}</td>
        <td className="py-2.5">{token.scope}</td>
        <td className="py-2.5 font-mono">
          {token.project ? (
            `${token.project}${token.environment ? `/${token.environment}` : ""}`
          ) : (
            <span className="font-sans text-mut">all projects</span>
          )}
        </td>
        <td className="py-2.5">
          <Actor name={token.created_by} kind={actorKind(token.created_by)} />
          {token.origin === "cli_login" && (
            <span className="text-mut"> via dock login</span>
          )}
        </td>
        <td
          className={`py-2.5 font-mono text-xs ${token.data ? "" : "font-sans text-mut"}`}
        >
          {tokenDataText(token.data)}
        </td>
        <td className={`py-2.5 ${token.last_used_at ? "" : "text-mut"}`}>
          {token.last_used_at ? ago(token.last_used_at) : "never"}
        </td>
        <td className="py-2.5 font-mono">
          {token.revoked_at ? (
            <span className="font-sans">revoked</span>
          ) : !token.expires_at ? (
            <span className="font-sans text-mut">never</span>
          ) : expired ? (
            <span className="font-sans">expired</span>
          ) : (
            isoDay(token.expires_at)
          )}
        </td>
        <td className="w-full py-1.5 text-right">
          <span className="inline-flex items-center gap-3">
            <Link to="/audit" search={{ actor: token.name }}>
              Activity
            </Link>
            {admin && !dead && (
              <Button small onClick={() => setEditingData((e) => !e)}>
                Data access
              </Button>
            )}
            {!token.revoked_at &&
              (confirming ? (
                <>
                  <Act
                    small
                    danger
                    cli={`dock token revoke ${token.id}`}
                    disabled={revoke.isPending}
                    onClick={() => revoke.mutate(token.id)}
                  >
                    Revoke now
                  </Act>
                  <Button small onClick={() => setConfirming(false)}>
                    Cancel
                  </Button>
                </>
              ) : (
                <Button small onClick={() => setConfirming(true)}>
                  Revoke
                </Button>
              ))}
            <ErrorText error={revoke.error} />
          </span>
        </td>
      </tr>
      {editingData && (
        <tr>
          <td colSpan={8}>
            <TokenDataEditor
              data={token.data}
              cap={tokenCap}
              cli={(data) =>
                data
                  ? `dock token update ${token.id} ${tokenDataFlags(data)}`
                  : `dock token update ${token.id} --data-clear`
              }
              onSave={(data) => saveData(token.id, data)}
              onDone={() => setEditingData(false)}
            />
          </td>
        </tr>
      )}
    </>
  );
}

/** What caps a token's own data access in Dock. */
const tokenCap =
  "never more than a user with its scope, or than its creator has. On production, write acts as propose.";

const scopes: Array<{ scope: Scope; note: string }> = [
  { scope: "read", note: "see status, releases, logs" },
  {
    scope: "deploy",
    note: "also deploy, restart, roll back, stage variables. Cannot reveal secrets or manage tokens and users.",
  },
  { scope: "admin", note: "everything, including tokens and users" },
];

const expiries = [
  { days: 90, label: "in 90 days" },
  { days: 30, label: "in 30 days" },
  { days: 7, label: "in 7 days" },
  { days: 365, label: "in a year" },
  { days: 0, label: "never" },
  { days: -1, label: "in … days" },
];

function CreateToken({
  onCreated,
}: {
  onCreated: (created: {
    name: string;
    secret: string;
    project?: string;
  }) => void;
}) {
  const create = useCreateToken();
  const projects = useProjects();
  const tree = useProjectTree();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("deploy");
  const [project, setProject] = useState("");
  const [environment, setEnvironment] = useState("");
  const [days, setDays] = useState(90);
  // Any number of days when "in … days" is picked (days -1).
  const [customDays, setCustomDays] = useState("14");
  const expires = days < 0 ? Math.max(0, Math.floor(Number(customDays))) : days;
  const [data, setData] = useState<DataTokenAccess>(null);
  const environments =
    tree
      .find((p) => p.project.name === project)
      ?.environments.map((e) => e.environment.name) ?? [];
  const cli = [
    `dock token create ${name || "<name>"} --scope ${scope}`,
    project && `-p ${project}`,
    project && environment && `-e ${environment}`,
    expires > 0 && `--expires ${expires}d`,
    tokenDataFlags(data),
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <section
      aria-labelledby="create"
      className="flex max-w-[760px] flex-col gap-4 rounded-panel border border-line p-4"
    >
      <h2 id="create" className="text-sm font-semibold">
        Create a token
      </h2>
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            {
              name: name.trim(),
              scope,
              ...(project && { project }),
              ...(project && environment && { environment }),
              ...(expires > 0 && { expires_in_days: expires }),
              ...(data && { data }),
            },
            {
              onSuccess: ({ token, secret }) => {
                setName("");
                onCreated({ name: token.name, secret, project: token.project });
              },
            },
          );
        }}
      >
        <label className="flex flex-col gap-1.5">
          <span>
            Name{" "}
            <span className="text-xs text-faint">shown in the audit log</span>
          </span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="agent-…"
            className="field py-[7px] font-mono"
          />
        </label>
        <fieldset>
          <legend className="mb-1.5">Scope</legend>
          <div className="flex flex-col gap-1.5">
            {scopes.map((s) => (
              <label key={s.scope} className="flex items-baseline gap-2">
                <input
                  type="radio"
                  className="accent-fg"
                  name="scope"
                  checked={scope === s.scope}
                  onChange={() => setScope(s.scope)}
                />
                <span>
                  <b className="font-medium">{s.scope}</b>{" "}
                  <span className="text-mut">{s.note}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-[1_1_200px] flex-col gap-1.5">
            <span>Limit to project</span>
            <select
              value={project}
              onChange={(e) => {
                setProject(e.target.value);
                setEnvironment("");
              }}
              className="field py-[7px]"
            >
              <option value="">all projects</option>
              {projects.map((p) => (
                <option key={p.id}>{p.name}</option>
              ))}
            </select>
          </label>
          <label className="flex flex-[1_1_200px] flex-col gap-1.5">
            <span>Limit to environment</span>
            <select
              value={environment}
              disabled={!project}
              onChange={(e) => setEnvironment(e.target.value)}
              className="field py-[7px]"
            >
              <option value="">all environments</option>
              {environments.map((e) => (
                <option key={e}>{e}</option>
              ))}
            </select>
          </label>
          <label className="flex flex-[1_1_200px] flex-col gap-1.5">
            <span>Expires</span>
            <select
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
              className="field py-[7px]"
            >
              {expiries.map((x) => (
                <option key={x.days} value={x.days}>
                  {x.label}
                </option>
              ))}
            </select>
            {days < 0 && (
              <input
                type="number"
                min={1}
                aria-label="Expires in days"
                value={customDays}
                onChange={(e) => setCustomDays(e.target.value)}
                className="field py-[5px] font-mono"
              />
            )}
          </label>
        </div>
        <TokenDataFields value={data} onChange={setData} cap={tokenCap} />
        <div className="flex flex-wrap items-center gap-3">
          <button
            type="submit"
            className="btn pri"
            disabled={!name.trim() || create.isPending}
          >
            Create token
          </button>
          <span className="font-mono text-2xs text-mut">{cli}</span>
          <ErrorText error={create.error} />
        </div>
      </form>
    </section>
  );
}
