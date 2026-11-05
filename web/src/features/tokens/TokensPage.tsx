import { useId, useState } from "react";
import { useCreateToken } from "../../data/mutations";
import {
  useIsOperator,
  useProjects,
  useProjectTree,
  useTokens,
} from "../../data/queries";
import type { Scope } from "../../data/types";
import { plural } from "../../ui/format";
import {
  Button,
  Check,
  CopyButton,
  ErrorText,
  Page,
  PageBar,
  SectionHead,
} from "../../ui/kit";
import { TokenDataFields, tokenDataFlags } from "../data/TokenData";
import type { DataTokenAccess } from "../data/types";
import { useOrgName } from "../org/data";
import { TokenTable, tokenCap } from "./TokenTable";

/**
 * The organization's tokens with create and revoke, in its settings
 * (admins). Each person's own are on their account page.
 */
export function TokensPage() {
  const tokens = useTokens();
  const live = tokens.filter((t) => !t.revoked_at);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<{
    name: string;
    secret: string;
    project?: string;
  }>();

  return (
    <>
      <PageBar
        parent={useOrgName()}
        title="Tokens"
        after={
          <span className="text-xs text-faint">
            {plural(live.length, "token")}
          </span>
        }
        actions={
          !creating && (
            <Button primary onClick={() => setCreating(true)}>
              Create token
            </Button>
          )
        }
      />
      <Page>
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
                This is the only time the secret is shown. If you lose it,
                create a new token.
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

        {creating && (
          <CreateToken
            onCreated={(c) => {
              setCreating(false);
              setCreated(c);
            }}
            onCancel={() => setCreating(false)}
          />
        )}

        <section aria-label="Tokens">
          <TokenTable tokens={tokens} />
        </section>

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
    </>
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
      <pre
        // biome-ignore lint/a11y/noNoninteractiveTabindex: it scrolls sideways, so the keyboard must reach it (WCAG scrollable region)
        tabIndex={0}
        className="overflow-x-auto rounded-ctl border border-line bg-side px-3 py-2 font-mono text-xs"
      >
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

const scopes: Array<{ scope: Scope; note: string }> = [
  { scope: "read", note: "see status, releases, logs" },
  {
    scope: "deploy",
    note: "also deploy, restart, roll back, stage variables. Cannot reveal secrets or manage tokens and users.",
  },
  {
    scope: "admin",
    note: "everything in the organization, including tokens and members",
  },
];

const expiries = [
  { days: 90, label: "in 90 days" },
  { days: 30, label: "in 30 days" },
  { days: 7, label: "in 7 days" },
  { days: 365, label: "in a year" },
  { days: 0, label: "never" },
  { days: -1, label: "in … days" },
];

/** The new token form, opened from the bar's Create token. */
function CreateToken({
  onCreated,
  onCancel,
}: {
  onCreated: (created: {
    name: string;
    secret: string;
    project?: string;
  }) => void;
  onCancel: () => void;
}) {
  const create = useCreateToken();
  // Set by a submit without a name; cleared by typing one.
  const [unnamed, setUnnamed] = useState(false);
  const nameId = useId();
  const operatorCaller = useIsOperator();
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
  // Operator tokens are unlimited admin tokens (ADR 0027).
  const [operatorToken, setOperatorToken] = useState(false);
  const operator = operatorToken && scope === "admin" && !project;
  const environments =
    tree
      .find((p) => p.project.name === project)
      ?.environments.map((e) => e.environment.name) ?? [];
  const cli = [
    `dock token create ${name || "<name>"} --scope ${scope}`,
    project && `-p ${project}`,
    project && environment && `-e ${environment}`,
    expires > 0 && `--expires ${expires}d`,
    operator && "--operator",
    tokenDataFlags(data),
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <section aria-labelledby="create" className="flex max-w-[760px] flex-col">
      <SectionHead id="create">Create a token</SectionHead>
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!name.trim()) {
            setUnnamed(true);
            document.getElementById(nameId)?.focus();
            return;
          }
          create.mutate(
            {
              name: name.trim(),
              scope,
              ...(project && { project }),
              ...(project && environment && { environment }),
              ...(expires > 0 && { expires_in_days: expires }),
              ...(data && { data }),
              ...(operator && { operator }),
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
            id={nameId}
            // biome-ignore lint/a11y/noAutofocus: opened by "Create token"
            autoFocus
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setUnnamed(false);
            }}
            placeholder="agent-…"
            aria-invalid={unnamed || undefined}
            aria-describedby={unnamed ? `${nameId}-error` : undefined}
            className={`field py-[7px] font-mono ${unnamed ? "border-fail" : ""}`}
          />
          {unnamed && (
            <span id={`${nameId}-error`} className="text-xs text-fail">
              Name the token.
            </span>
          )}
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
            {operatorCaller && scope === "admin" && (
              <label className="flex items-baseline gap-2">
                <input
                  type="checkbox"
                  className="accent-fg"
                  checked={operatorToken}
                  disabled={!!project}
                  onChange={(e) => setOperatorToken(e.target.checked)}
                />
                <span>
                  <b className="font-medium">operator</b>{" "}
                  <span className="text-mut">
                    also runs the platform: servers, backups, accounts. No
                    project limit
                  </span>
                </span>
              </label>
            )}
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
        <div className="flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 text-xs">
            <ErrorText error={create.error} />
          </span>
          <Button onClick={onCancel}>Cancel</Button>
          <button type="submit" className="btn pri" disabled={create.isPending}>
            Create token
          </button>
        </div>
        <code className="border-t border-line pt-2.5 font-mono text-2xs break-all text-faint">
          {cli}
        </code>
      </form>
    </section>
  );
}
