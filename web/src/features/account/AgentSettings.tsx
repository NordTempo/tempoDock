import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { AgentMatrix } from "../access/AgentMatrix";
import { useOwnTokens } from "../tokens/data";

// Your agents (ADR 0080, phase 6): what your `dock login` tokens may use
// of your access in this organization, per area role and environment
// kind, and where they ask you first. All your agents at once, or one
// narrowed further. An agent can tighten this itself; loosening is here.

type Setting = components["schemas"]["AgentSetting"];

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

/** The caller's setting for all their agents (0) or one, editable. */
function Matrix({ agent }: { agent: number }) {
  const saved = useAgentSettings(agent);
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
  return (
    <AgentMatrix
      saved={saved.settings ?? []}
      pending={save.isPending}
      error={save.error}
      cli={(c) =>
        `dock agent set ${c.area}=${c.level} --kinds ${c.kind}${c.ask ? " --ask" : " --no-ask"}${agent ? ` --agent ${agent}` : ""}`
      }
      onSave={(changed) => save.mutate(changed)}
    />
  );
}
