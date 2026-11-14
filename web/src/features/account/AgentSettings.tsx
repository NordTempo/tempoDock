import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { Act, ErrorText } from "../../ui/kit";
import { useRoles } from "../access/data";
import { useOwnTokens } from "../tokens/data";

// Your agents (ADR 0080, phase 6): what your `dock login` tokens may use
// of your access in this organization, per area role and environment
// kind, and where they ask you first. All your agents at once, or one
// narrowed further. An agent can tighten this itself; loosening is here.

type Setting = components["schemas"]["AgentSetting"];
const kinds = ["production", "other", "previews"] as const;

const settingsKey = (agent: number) => ["access", "agent-settings", agent];

/** The setting matrix for all agents (0) or one. */
function useAgentSettings(agent: number) {
  return useSuspenseQuery({
    queryKey: settingsKey(agent),
    queryFn: () =>
      body(
        api.GET("/api/v1/me/agent-settings", {
          params: { query: { agent } },
        }),
      ),
  }).data;
}

export function AgentSettings() {
  const agents = useOwnTokens().filter((t) => t.origin === "cli_login");
  const [agent, setAgent] = useState(0);
  return (
    <div className="flex flex-col gap-3">
      <label className="flex items-center gap-2">
        <span className="text-mut">For</span>
        <select
          className="field"
          value={agent}
          onChange={(e) => setAgent(Number(e.target.value))}
        >
          <option value={0}>all your agents</option>
          {agents.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name} only (narrower)
            </option>
          ))}
        </select>
      </label>
      <Matrix key={agent} agent={agent} />
    </div>
  );
}

/** The editable matrix: an area per row, a kind per column. */
function Matrix({ agent }: { agent: number }) {
  const saved = useAgentSettings(agent);
  const roles = useRoles();
  const [cells, setCells] = useState<Setting[]>(saved.settings ?? []);
  const save = useAction(
    (settings: Setting[]) =>
      body(
        api.PUT("/api/v1/me/agent-settings", {
          body: { agent, settings },
        }),
      ),
    () => [["access", "agent-settings"]],
    () => "Saved what your agents may do",
  );
  const levelsOf = (area: string) => {
    const r = roles.find((x) => x.key === area);
    return (r?.levels ?? []).map((l) => l.name || "yes").filter(Boolean);
  };
  const changed = cells.filter((c, i) => {
    const s = saved.settings?.[i];
    return s && (s.level !== c.level || !!s.ask !== !!c.ask);
  });
  const cli = changed
    .map(
      (c) =>
        `dock agent set ${c.area}=${c.level} --kinds ${c.kind}${c.ask ? " --ask" : " --no-ask"}${agent ? ` --agent ${agent}` : ""}`,
    )
    .join("; ");
  const update = (i: number, patch: Partial<Setting>) =>
    setCells(cells.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const areas = [...new Set(cells.map((c) => c.area))];
  return (
    <>
      <table>
        <thead>
          <tr>
            <th>Area</th>
            {kinds.map((k) => (
              <th key={k}>{k}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {areas.map((area) => (
            <tr key={area}>
              <td className="py-1.5">
                {roles.find((r) => r.key === area)?.name ?? area}
              </td>
              {kinds.map((k) => {
                const i = cells.findIndex(
                  (c) => c.area === area && c.kind === k,
                );
                const c = cells[i];
                if (!c) return <td key={k} />;
                return (
                  <td key={k} className="py-1.5">
                    <span className="flex items-center gap-2">
                      <select
                        className="field"
                        aria-label={`${area} in ${k}`}
                        value={c.level}
                        onChange={(e) => update(i, { level: e.target.value })}
                      >
                        <option value="off">off</option>
                        {levelsOf(area).map((l) => (
                          <option key={l} value={l === "yes" ? "" : l}>
                            {l}
                          </option>
                        ))}
                        <option value="all">all</option>
                      </select>
                      <label className="flex items-center gap-1 text-mut">
                        <input
                          type="checkbox"
                          className="accent-fg"
                          checked={!!c.ask}
                          onChange={(e) => update(i, { ask: e.target.checked })}
                        />
                        ask
                      </label>
                    </span>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div>
        <Act
          primary
          cli={cli || "dock agent set AREA=LEVEL --kinds KIND"}
          disabled={changed.length === 0 || save.isPending}
          onClick={() => save.mutate(changed)}
        >
          Save
        </Act>
      </div>
      {save.error && <ErrorText error={save.error} />}
    </>
  );
}
