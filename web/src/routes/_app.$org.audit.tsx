import { createFileRoute, redirect } from "@tanstack/react-router";
import { validateAuditSearch } from "../features/audit/search";

// The organization's old Activity address: the Activity section of
// Organization settings now, with the filters it was opened with.
export const Route = createFileRoute("/_app/$org/audit")({
  validateSearch: validateAuditSearch,
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/$org/settings/activity",
      params,
      search,
      replace: true,
    });
  },
});
