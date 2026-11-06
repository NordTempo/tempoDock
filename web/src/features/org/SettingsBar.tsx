import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useIsAdmin } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { PageBar } from "../../ui/kit";

/**
 * The header of every organization settings page: one title, and the
 * sections as tabs under it (tabs don't add a breadcrumb level). The
 * sidebar stays the organization's; `actions` are the section's own.
 * Sections only admins can use are hidden from everyone else.
 */
export function OrgSettingsBar({ actions }: { actions?: ReactNode }) {
  const org = useOrg();
  const admin = useIsAdmin();
  const params = { org };
  return (
    <>
      <PageBar title="Organization settings" actions={actions} />
      <nav
        aria-label="Organization settings"
        className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap"
      >
        <Link to="/$org/settings/general" params={params}>
          General
        </Link>
        {admin && (
          <Link to="/$org/settings/members" params={params}>
            Members
          </Link>
        )}
        <Link to="/$org/settings/variables" params={params}>
          Variables
        </Link>
        {admin && (
          <>
            <Link to="/$org/settings/tokens" params={params}>
              Tokens
            </Link>
            <Link to="/$org/settings/registries" params={params}>
              Registries
            </Link>
            <Link to="/$org/settings/sso" params={params}>
              Single sign-on
            </Link>
          </>
        )}
      </nav>
    </>
  );
}
