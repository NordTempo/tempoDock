import { useState } from "react";
import { useIsAdmin } from "../../data/queries";
import type { EnvRef } from "../../data/types";
import { since } from "../../ui/format";
import { Act, Button, ErrorText, Panel, SectionHead } from "../../ui/kit";
import {
  type LogDrain,
  type NewLogDrain,
  useAddLogDrain,
  useLogDrains,
  useRemoveLogDrain,
  useTestLogDrain,
} from "./data";

// An environment's log drains (ADR 0046): where its services' output is
// sent, how delivery goes, and an inline form to add one. Admins change
// them; everyone who can read the environment sees them.

type Kind = NewLogDrain["kind"];

const placeholders: Record<Kind, string> = {
  http: "https://logs.example.com/ingest",
  syslog: "tls://logs.example.com:6514",
  loki: "https://loki.example.com",
};

/**
 * One environment's log drains, as a section of the project settings'
 * Log drains tab (one per environment, headed by its name).
 */
export function LogDrains({ env }: { env: EnvRef }) {
  const drains = useLogDrains(env);
  const isAdmin = useIsAdmin();
  const [adding, setAdding] = useState(false);
  const head = `drains-${env.environment}-head`;
  return (
    <section
      aria-labelledby={head}
      id={`drains-${env.environment}`}
      className="scroll-mt-[64px]"
    >
      <SectionHead
        id={head}
        right={
          isAdmin &&
          !adding && (
            <Button small onClick={() => setAdding(true)}>
              Add drain
            </Button>
          )
        }
      >
        {env.environment}
      </SectionHead>
      {drains.length > 0 ? (
        <Panel>
          <table className="w-full">
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Target</th>
                <th className="text-right">Sent</th>
                <th className="text-right">Dropped</th>
                <th>Last sent</th>
                <th>Last error</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {drains.map((d) => (
                <DrainRow key={d.id} env={env} drain={d} admin={isAdmin} />
              ))}
            </tbody>
          </table>
        </Panel>
      ) : (
        !adding && <p className="py-1 text-mut">No drains.</p>
      )}
      {adding && <AddDrain env={env} onClose={() => setAdding(false)} />}
    </section>
  );
}

function DrainRow({
  env,
  drain: d,
  admin,
}: {
  env: EnvRef;
  drain: LogDrain;
  admin: boolean;
}) {
  const test = useTestLogDrain(env);
  const remove = useRemoveLogDrain(env);
  const [confirming, setConfirming] = useState(false);
  const st = d.status;
  const flags = `-e ${env.environment}`;
  return (
    <tr>
      <td className="py-2 font-mono">{d.name}</td>
      <td className="py-2">{d.kind}</td>
      <td className="py-2 font-mono">{d.target}</td>
      <td className="py-2 text-right font-mono">{st.sent.toLocaleString()}</td>
      <td
        className={`py-2 text-right font-mono ${st.dropped ? "text-warn" : "text-mut"}`}
      >
        {st.dropped.toLocaleString()}
      </td>
      <td className="py-2 text-mut">
        {st.last_sent_at ? `${since(st.last_sent_at)} ago` : "never"}
      </td>
      <td className="w-full py-2 whitespace-normal">
        {st.last_error ? (
          <span className="text-xs text-fail">{st.last_error}</span>
        ) : (
          <span className="text-mut">none</span>
        )}
      </td>
      <td className="w-px py-1.5 text-right whitespace-nowrap">
        {admin && (
          <span className="inline-flex items-center gap-3">
            {test.data && (
              <span className={test.data.ok ? "text-ok" : "text-fail"}>
                {test.data.ok
                  ? "Sent"
                  : `Not sent${test.data.error ? `: ${test.data.error}` : ""}`}
              </span>
            )}
            <ErrorText error={test.error ?? remove.error} />
            <Act
              small
              cli={`dock logs drain test ${d.name} ${flags}`}
              disabled={test.isPending}
              onClick={() => test.mutate(d.id)}
            >
              Send test
            </Act>
            {confirming ? (
              <>
                <Act
                  small
                  danger
                  cli={`dock logs drain remove ${d.name} ${flags}`}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(d.id)}
                >
                  Remove now
                </Act>
                <Button small onClick={() => setConfirming(false)}>
                  Cancel
                </Button>
              </>
            ) : (
              <Button danger small onClick={() => setConfirming(true)}>
                Remove
              </Button>
            )}
          </span>
        )}
      </td>
    </tr>
  );
}

function AddDrain({ env, onClose }: { env: EnvRef; onClose: () => void }) {
  const add = useAddLogDrain(env);
  const [kind, setKind] = useState<Kind>("http");
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [header, setHeader] = useState("");
  const [token, setToken] = useState("");
  const field = "field py-[7px]";
  const cli = [
    `dock logs drain add ${kind} -e ${env.environment}`,
    "-",
    name.trim() && `--name ${name.trim()}`,
    header.trim() && `--header ${header.trim().split("=")[0]}=…`,
    token && "--token …",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <form
      aria-label="Add log drain"
      className="flex max-w-[760px] flex-col gap-3 border-t border-line pt-3"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        const input: NewLogDrain = { kind, url: url.trim() };
        if (name.trim()) input.name = name.trim();
        const [k, ...v] = header.split("=");
        if (kind !== "syslog" && k?.trim()) {
          input.headers = { [k.trim()]: v.join("=").trim() };
        }
        if (kind !== "syslog" && token) input.token = token;
        add.mutate(input, { onSuccess: onClose });
      }}
    >
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[0_1_120px] flex-col gap-1.5">
          <span>Kind</span>
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as Kind)}
            className={field}
          >
            <option value="http">http</option>
            <option value="syslog">syslog</option>
            <option value="loki">loki</option>
          </select>
        </label>
        <label className="flex flex-[3_1_320px] flex-col gap-1.5">
          <span>
            URL <span className="text-mut">stored encrypted, never shown</span>
          </span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the form opens on request
            autoFocus
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={placeholders[kind]}
            className={`${field} font-mono`}
          />
        </label>
        <label className="flex flex-[1_1_140px] flex-col gap-1.5">
          <span>
            Name <span className="text-mut">default {kind}</span>
          </span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className={`${field} font-mono`}
          />
        </label>
      </div>
      {kind !== "syslog" && (
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-[1_1_240px] flex-col gap-1.5">
            <span>
              Header <span className="text-mut">optional, Name=value</span>
            </span>
            <input
              value={header}
              onChange={(e) => setHeader(e.target.value)}
              placeholder="X-Api-Key=…"
              autoComplete="off"
              className={`${field} font-mono`}
            />
          </label>
          <label className="flex flex-[1_1_240px] flex-col gap-1.5">
            <span>
              Bearer token <span className="text-mut">optional</span>
            </span>
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="off"
              className={`${field} font-mono`}
            />
          </label>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={cli}
          disabled={add.isPending || !url.trim()}
          reason={add.isPending ? undefined : "Enter the drain's URL first."}
        >
          Add drain
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <ErrorText error={add.error} />
      </div>
    </form>
  );
}
