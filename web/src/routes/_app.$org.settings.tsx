import { createFileRoute, Outlet } from "@tanstack/react-router";
import { OrgSettingsColumn } from "../features/org/SettingsBar";
import { PageBar } from "../ui/kit";

// Organization settings (ADR 0062, mock S2): the page bar, the grouped
// column of sections, and the open section beside it.
export const Route = createFileRoute("/_app/$org/settings")({
  staticData: { ownMenu: true },
  component: OrgSettings,
});

function OrgSettings() {
  return (
    <>
      <PageBar title="Organization settings" />
      <div className="flex min-w-0 flex-1 flex-col md:flex-row">
        <OrgSettingsColumn />
        <div className="flex min-w-0 flex-1 flex-col">
          <Outlet />
        </div>
      </div>
    </>
  );
}
