import { useSuspenseQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { Page } from "../../ui/kit";
import { AgentMatrix } from "./AgentMatrix";
import { ShadowNote } from "./ShadowNote";

// Settings › Agent rules (ADR 0080, 6c): the most any of the
// organization's agents (people's `dock login` and its tokens) may use,
// per area role and environment kind, and where their changes ask an
// approver first. A token can bypass them (its Grants and groups).

export const agentRulesQuery = {
  queryKey: ["access", "agent-rules"],
  queryFn: () => body(api.GET("/api/v1/agent-rules")),
};

export function AgentRulesPage() {
  const saved = useSuspenseQuery(agentRulesQuery).data;
  const save = useAction(
    (settings: NonNullable<typeof saved.settings>) =>
      body(api.PUT("/api/v1/agent-rules", { body: { settings } })),
    () => [["access", "agent-rules"]],
    () => "Saved the agent rules",
  );
  return (
    <Page>
      <ShadowNote />
      <AgentMatrix
        saved={saved.settings ?? []}
        pending={save.isPending}
        error={save.error}
        cli={(c) =>
          `dock agent rule ${c.area}=${c.level} --kinds ${c.kind}${c.ask ? " --ask" : " --no-ask"}`
        }
        onSave={(changed) => save.mutate(changed)}
      />
    </Page>
  );
}
