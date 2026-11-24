import { createFileRoute, Outlet } from "@tanstack/react-router";
import { OrgSettingsColumn } from "../features/org/SettingsBar";

// Organization settings (ADR 0062, mock S2; ADR 0099): the grouped column
// of sections, and the open section beside it with its own bar (its name
// as the title, its actions), the shape of every Settings page in Dock.
export const Route = createFileRoute("/_app/$org/settings")({
  staticData: { ownMenu: true },
  component: OrgSettings,
});

function OrgSettings() {
  return (
    <div className="flex min-w-0 flex-1 flex-col md:flex-row">
      <OrgSettingsColumn />
      <div className="flex min-w-0 flex-1 flex-col">
        <Outlet />
      </div>
    </div>
  );
}
