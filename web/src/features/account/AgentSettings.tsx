import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { useOrg } from "../../shell/location";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { AgentMatrix } from "../access/AgentMatrix";
import { useOwnTokens } from "../tokens/data";
import { liveAgents } from "./agents";

// Your agents (ADR 0080, phase 6): what your `dock login` tokens may use
// of your access in this organization, per area role and environment
// kind, and where they ask you first. All your agents at once, or one
// narrowed further. An agent can tighten this itself; loosening is here.

type Setting = components["schemas"]["AgentSetting"];

const settingsKey = (agent: number) => ["access", "agent-settings", agent];

/**
 * The setting matrix for all agents (0) or one. Not a suspense query: a
 * failure (an agent revoked meanwhile answers 404) is this panel's to
 * show, where a thrown one becomes the route's and replaces the page.
 */
function useAgentSettings(agent: number) {
  return useQuery({
    queryKey: settingsKey(agent),
    queryFn: () =>
      body(
        api.GET("/api/v1/me/agent-settings", {
          params: { query: { agent } },
        }),
      ),
  });
}

export function AgentSettings() {
  const agents = liveAgents(useOwnTokens());
  const org = useOrg();
  const [picked, setAgent] = useState(0);
  // An agent revoked while picked is no longer one: back to all of them.
  const agent = agents.some((t) => t.id === picked) ? picked : 0;
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
      <p className="text-mut">
        In the Data panel your agents get what{" "}
        <Link to="/$org/settings/data-access" params={{ org }}>
          the data access policy
        </Link>{" "}
        gives you, up to the level set here.
      </p>
    </div>
  );
}

/** The caller's setting for all their agents (0) or one, editable. */
function Matrix({ agent }: { agent: number }) {
  const { data: saved, error, refetch } = useAgentSettings(agent);
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
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!saved) return <p className="text-faint">Loading</p>;
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
