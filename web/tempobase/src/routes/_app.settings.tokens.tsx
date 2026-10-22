import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  Btn,
  CopyBtn,
  ErrorText,
  Head,
  Kbd,
} from "../../../src/features/data/kit";
import type { DataTokenAccess } from "../../../src/features/data/types";
import { useShortcuts } from "../../../src/shell/keys";
import { ago, day } from "../../../src/ui/format";
import type { Token } from "../api/types";
import {
  queries,
  useCreateToken,
  useDeleteToken,
  useIsAdmin,
  useMe,
  useTokens,
  useUpdateToken,
} from "../data/queries";

// Agents and MCP (G7): the MCP endpoint, the caller's tokens (every
// token for admins), and a pane to create one or change what it may do.
export const Route = createFileRoute("/_app/settings/tokens")({
  component: Agents,
});

/**
 * The most an agent may do, as the token's own data access. "policy"
 * follows the agents rule; the others override it, and every one is still
 * capped by the creator's role (and production writes are proposals).
 */
const tiers = {
  policy: { label: "Policy", data: null },
  read: {
    label: "Read",
    data: { non_production: "read", production: "read", sensitive: "masked" },
  },
  propose: {
    label: "Propose",
    data: {
      non_production: "propose",
      production: "propose",
      sensitive: "masked",
    },
  },
  same: {
    label: "Same as me",
    data: {
      non_production: "write",
      production: "write",
      sensitive: "visible",
    },
  },
} as const satisfies Record<string, { label: string; data: DataTokenAccess }>;
type Tier = keyof typeof tiers;
const tierKeys = Object.keys(tiers).filter((k): k is Tier => k in tiers);

const tierNotes: Record<Tier, string> = {
  policy:
    "What the policy gives agents (Access). Nothing until an admin allows it.",
  read: "Look at data and run select queries. Nothing it does can change rows.",
  propose:
    "Also draft changes. An admin approves each one before it touches the data.",
  same: "What you may do, never more. On production its changes are proposals.",
};

/** The tier a token's data access matches; undefined: set some other way. */
function tierOf(data: DataTokenAccess | undefined): Tier | undefined {
  const same = JSON.stringify(data ?? null);
  return tierKeys.find((t) => JSON.stringify(tiers[t].data) === same);
}

/** A token's own levels when they match no tier. */
const levelsText = (d: DataTokenAccess | undefined) =>
  d ? `${d.non_production}, production ${d.production}` : "Policy";

/** The MCP endpoint for this server. */
const endpoint = () => `${location.origin}/api/v1/mcp`;

function Agents() {
  const admin = useIsAdmin();
  const me = useMe();
  const tokens = useTokens();
  const [open, setOpen] = useState<number | "new" | null>(null);
  useShortcuts([{ key: "n", label: "new agent", run: () => setOpen("new") }]);
  const live = (tokens.data ?? []).filter((t) => !t.revoked_at);
  const mine = live.filter((t) => t.user === me.email);
  const others = live.filter((t) => t.user !== me.email);
  const selected = live.find((t) => t.id === open);

  return (
    <>
      <Head
        title="Agents & MCP"
        actions={
          <Btn primary onClick={() => setOpen("new")}>
            New agent <Kbd>N</Kbd>
          </Btn>
        }
      >
        <span className="text-xs text-(--g-mut)">
          An agent acts as you, never with more access than you
        </span>
      </Head>
      <div className="flex min-h-0 flex-1 flex-wrap">
        <main className="min-w-0 flex-[999_1_460px] px-5 pt-6 pb-10">
          <div className="mx-auto flex max-w-[680px] flex-col gap-7">
            <section className="flex flex-col gap-2">
              <h2 className="text-[13px] font-semibold">MCP endpoint</h2>
              <div className="g-box">
                <div className="flex min-h-11 flex-wrap items-center gap-2.5 px-3">
                  <code className="flex-1 text-xs break-all text-(--g-code)">
                    {endpoint()}
                  </code>
                  <CopyBtn text={endpoint()} />
                </div>
                <p className="px-3 py-2.5 text-xs text-(--g-mut)">
                  One address for every agent. Its token decides who it is and
                  what it can do.
                </p>
              </div>
            </section>
            <ErrorText error={tokens.error} />
            <AgentList
              title="Your agents"
              tokens={mine}
              open={open}
              onOpen={setOpen}
              empty="None yet. Create one for each tool you connect, so you can revoke them one by one."
            />
            {admin && others.length > 0 && (
              <AgentList
                title="Everyone else's"
                tokens={others}
                open={open}
                onOpen={setOpen}
                owner
              />
            )}
          </div>
        </main>
        {open === "new" ? (
          <NewAgent
            onDone={(id) => setOpen(id)}
            onClose={() => setOpen(null)}
          />
        ) : selected ? (
          <AgentPane
            key={selected.id}
            token={selected}
            onClose={() => setOpen(null)}
          />
        ) : null}
      </div>
    </>
  );
}

function AgentList({
  title,
  tokens,
  open,
  onOpen,
  owner,
  empty,
}: {
  title: string;
  tokens: Token[];
  open: number | "new" | null;
  onOpen: (id: number) => void;
  owner?: boolean;
  empty?: string;
}) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-[13px] font-semibold">
        {title}{" "}
        <span className="font-normal text-(--g-mut)">{tokens.length}</span>
      </h2>
      {tokens.length === 0 ? (
        <p className="text-(--g-mut)">{empty}</p>
      ) : (
        <ul className="g-box overflow-hidden">
          {tokens.map((t) => {
            const tier = tierOf(t.data);
            return (
              <li key={t.id}>
                <button
                  type="button"
                  aria-current={t.id === open}
                  onClick={() => onOpen(t.id)}
                  className="grid min-h-11 w-full grid-cols-[minmax(0,1fr)_150px_90px] items-center px-3 text-left hover:bg-(--g-hover) aria-[current=true]:bg-(--g-raise-2)"
                >
                  <span className="truncate">
                    <span className="font-medium">{t.name}</span>
                    {owner && (
                      <span className="ml-1.5 text-xs text-(--g-mut)">
                        {t.user}
                      </span>
                    )}
                  </span>
                  <span className="truncate text-(--g-dim)">
                    {tier ? tiers[tier].label : levelsText(t.data)}
                    {t.connections && ` · ${t.connections.join(", ")}`}
                  </span>
                  <span className="text-right text-xs text-(--g-mut)">
                    {t.expires_at
                      ? `until ${day(t.expires_at)}`
                      : ago(t.created_at)}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** The side pane (G7). */
function Pane({
  title,
  sub,
  onClose,
  children,
}: {
  title: string;
  sub: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <aside
      aria-label="Agent"
      className="g-aside max-w-[320px] flex-[1_1_300px]"
    >
      <div className="flex h-7 items-center justify-between text-xs text-(--g-mut)">
        Agent
        <Btn small ghost aria-label="Close" onClick={onClose}>
          ×
        </Btn>
      </div>
      <div className="mt-1 text-base font-semibold">{title}</div>
      <div className="mb-4 text-xs text-(--g-mut)">{sub}</div>
      {children}
    </aside>
  );
}

/** Policy, Read, Propose, Same as me. */
function TierChoice({
  value,
  onChange,
}: {
  value: Tier | undefined;
  onChange: (tier: Tier) => void;
}) {
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="g-label mb-2">The most it can do</legend>
      <div className="g-seg grid-cols-4">
        {tierKeys.map((t) => (
          <button
            key={t}
            type="button"
            aria-pressed={t === value}
            className="px-1!"
            onClick={() => onChange(t)}
          >
            {tiers[t].label}
          </button>
        ))}
      </div>
      <p className="text-xs text-(--g-mut)">
        {value ? tierNotes[value] : "Set to its own levels through the API."}
      </p>
    </fieldset>
  );
}

function AgentPane({
  token: t,
  onClose,
}: {
  token: Token;
  onClose: () => void;
}) {
  const update = useUpdateToken();
  const remove = useDeleteToken();
  const tier = tierOf(t.data);
  return (
    <Pane
      title={t.name}
      sub={`Acts as ${t.user} · created ${day(t.created_at)}`}
      onClose={onClose}
    >
      <TierChoice
        value={tier}
        onChange={(next) => update.mutate({ id: t.id, data: tiers[next].data })}
      />
      <ErrorText error={update.error} />
      <div className="mt-5 flex flex-col gap-1">
        <span className="g-label">Connections</span>
        <span>
          {t.connections?.join(", ") ?? "Every one the policy allows"}
        </span>
      </div>
      <Snippet secret={null} />
      <div className="mt-auto flex flex-wrap items-center gap-2 border-t border-(--g-line) pt-5">
        <code className="flex-1 text-xs text-(--g-mut)">{t.prefix}••••</code>
        <Btn
          small
          ghost
          danger
          disabled={remove.isPending}
          onClick={() => remove.mutate(t.id, { onSuccess: onClose })}
        >
          Revoke
        </Btn>
        <ErrorText error={remove.error} />
      </div>
    </Pane>
  );
}

const expiries = [
  [undefined, "Never"],
  [30, "30 days"],
  [90, "90 days"],
  [365, "1 year"],
] as const;

function NewAgent({
  onDone,
  onClose,
}: {
  onDone: (id: number) => void;
  onClose: () => void;
}) {
  const connections = useQuery(queries.connections());
  const create = useCreateToken();
  const [name, setName] = useState("");
  const [tier, setTier] = useState<Tier>("read");
  const [only, setOnly] = useState<string[]>([]);
  const [days, setDays] = useState<number | undefined>(undefined);
  const made = create.data;

  if (made) {
    return (
      <Pane
        title={made.token.name}
        sub="Created"
        onClose={() => onDone(made.token.id)}
      >
        <p className="text-xs text-(--g-dim)">
          Its token, shown once. Put it in the agent's MCP settings now.
        </p>
        <div className="mt-2 flex items-center gap-2">
          <code className="min-w-0 flex-1 text-xs break-all text-(--g-code)">
            {made.secret}
          </code>
          <CopyBtn text={made.secret} />
        </div>
        <Snippet secret={made.secret} />
        <div className="mt-auto pt-5">
          <Btn onClick={() => onDone(made.token.id)}>Done</Btn>
        </div>
      </Pane>
    );
  }

  return (
    <Pane
      title="New agent"
      sub="One per tool, so each can be revoked"
      onClose={onClose}
    >
      <form
        className="flex flex-col gap-5"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate({
            name: name.trim(),
            connections: only.length ? only : null,
            data: tiers[tier].data,
            expires_in_days: days,
          });
        }}
      >
        <label className="flex flex-col gap-1.5">
          <span className="g-label">Name</span>
          <input
            className="g-field h-8"
            required
            // biome-ignore lint/a11y/noAutofocus: opened on request
            autoFocus
            placeholder="Claude Desktop"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <TierChoice value={tier} onChange={setTier} />
        <fieldset className="flex flex-col gap-1">
          <legend className="g-label mb-1">Connections</legend>
          {connections.data?.map((c) => (
            <label key={c.name} className="flex h-7 items-center gap-2">
              <input
                type="checkbox"
                checked={only.includes(c.name)}
                onChange={(e) =>
                  setOnly((o) =>
                    e.target.checked
                      ? [...o, c.name]
                      : o.filter((n) => n !== c.name),
                  )
                }
              />
              {c.name}
            </label>
          ))}
          <span className="text-xs text-(--g-mut)">
            None ticked: every one the policy allows.
          </span>
        </fieldset>
        <label className="flex flex-col gap-1.5">
          <span className="g-label">Expires</span>
          <select
            className="g-field h-8"
            value={days ?? ""}
            onChange={(e) =>
              setDays(e.target.value ? Number(e.target.value) : undefined)
            }
          >
            {expiries.map(([d, label]) => (
              <option key={label} value={d ?? ""}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <div className="flex items-center gap-2">
          <Btn primary type="submit" disabled={create.isPending}>
            Create agent
          </Btn>
          <ErrorText error={create.error} />
        </div>
      </form>
    </Pane>
  );
}

/**
 * How to connect an MCP client: Claude Code's command and the JSON most
 * clients take. With the secret right after creating; a placeholder after.
 */
function Snippet({ secret }: { secret: string | null }) {
  const token = secret ?? "<token>";
  const command = `claude mcp add --transport http tempobase ${endpoint()} --header "Authorization: Bearer ${token}"`;
  const json = JSON.stringify(
    {
      mcpServers: {
        tempobase: {
          type: "http",
          url: endpoint(),
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  );
  return (
    <details className="mt-5" open={!!secret}>
      <summary className="cursor-pointer text-xs text-(--g-dim)">
        Connect a client
      </summary>
      <div className="mt-2 flex flex-col gap-2">
        <span className="g-label">Claude Code</span>
        <pre className="rounded-lg bg-(--g-raise) p-2.5 text-[11.5px] leading-[19px] whitespace-pre-wrap break-all text-(--g-code)">
          {command}
        </pre>
        <CopyBtn text={command} label="Copy command" />
        <span className="g-label mt-2">Other clients (mcpServers JSON)</span>
        <pre className="rounded-lg bg-(--g-raise) p-2.5 text-[11.5px] leading-[19px] whitespace-pre-wrap break-all text-(--g-code)">
          {json}
        </pre>
        <CopyBtn text={json} label="Copy JSON" />
      </div>
    </details>
  );
}
