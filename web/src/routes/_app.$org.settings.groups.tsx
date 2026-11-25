import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { accessQueries } from "../features/access/data";
import { GroupsPage } from "../features/access/GroupsPage";
import { orgQueries } from "../features/org/data";

// Settings › Groups (ADR 0080): groups, what they give, who is in them.
export const Route = createFileRoute("/_app/$org/settings/groups")({
  validateSearch: (s: Record<string, unknown>): { group?: string } => ({
    group: typeof s.group === "string" ? s.group : undefined,
  }),
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(accessQueries.groups()),
      queryClient.ensureQueryData(accessQueries.roles()),
      queryClient.ensureQueryData(accessQueries.customRoles()),
      queryClient.ensureQueryData(orgQueries.members()),
    ]),
  component: Groups,
});

function Groups() {
  const { group } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  return (
    <GroupsPage
      selected={group}
      onSelect={(g) =>
        navigate({ search: g ? { group: g } : {}, replace: true })
      }
    />
  );
}
