import { createFileRoute } from "@tanstack/react-router";
import {
  AgentRulesPage,
  agentRulesQuery,
} from "../features/access/AgentRulesPage";
import { accessQueries } from "../features/access/data";

// Settings › Agent rules (ADR 0080): the organization's ceilings for its agents.
export const Route = createFileRoute("/_app/$org/settings/agents")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(accessQueries.roles()),
      queryClient.ensureQueryData(agentRulesQuery),
    ]),
  component: AgentRulesPage,
});
