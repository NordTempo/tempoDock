import { Link, useLocation } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useIsAdmin } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { PageBar } from "../../ui/kit";

/** The settings sections, in tab order; some are for admins only. */
const sections = [
  { to: "/$org/settings/general", label: "General", admin: false },
  { to: "/$org/settings/members", label: "Members", admin: true },
  { to: "/$org/settings/variables", label: "Variables", admin: false },
  { to: "/$org/settings/tokens", label: "Tokens", admin: true },
  { to: "/$org/settings/registries", label: "Registries", admin: true },
  { to: "/$org/settings/sso", label: "Single sign-on", admin: true },
] as const;

/**
 * The header of every organization settings page: the section as the
 * title under "Organization settings" (so the sidebar's Members opens a
 * page called Members), and the sections as tabs under it. The sidebar
 * stays the organization's; `actions` are the section's own. Sections
 * only admins can use are hidden from everyone else.
 */
export function OrgSettingsBar({ actions }: { actions?: ReactNode }) {
  const org = useOrg();
  const admin = useIsAdmin();
  const params = { org };
  const leaf = useLocation({ select: (l) => l.pathname.split("/").pop() });
  const shown = sections.filter((s) => admin || !s.admin);
  const current = shown.find((s) => s.to.endsWith(`/${leaf}`));
  return (
    <>
      <PageBar
        parent={
          <Link
            to="/$org/settings/general"
            params={params}
            className="hover:text-fg"
          >
            Organization settings
          </Link>
        }
        title={current?.label ?? "Organization settings"}
        actions={actions}
      />
      <nav
        aria-label="Organization settings"
        className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap"
      >
        {shown.map((s) => (
          <Link key={s.to} to={s.to} params={params}>
            {s.label}
          </Link>
        ))}
      </nav>
    </>
  );
}
