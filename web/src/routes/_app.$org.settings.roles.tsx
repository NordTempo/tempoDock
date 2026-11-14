import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { accessQueries } from "../features/access/data";
import { RolesPage } from "../features/access/RolesPage";

// Settings › Roles (ADR 0080): the built-in roles and the org's own.
export const Route = createFileRoute("/_app/$org/settings/roles")({
  validateSearch: (s: Record<string, unknown>): { role?: string } =>
    typeof s.role === "string" ? { role: s.role } : {},
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(accessQueries.roles()),
      queryClient.ensureQueryData(accessQueries.customRoles()),
    ]),
  component: Roles,
});

function Roles() {
  const { role } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  return (
    <RolesPage
      selected={role}
      onSelect={(r) =>
        navigate({ search: r ? { role: r } : {}, replace: true })
      }
    />
  );
}
