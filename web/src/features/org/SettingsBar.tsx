import { useQuery } from "@tanstack/react-query";
import { Link, useLocation } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { queries, useIsAdmin } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { useOrgOpenAlerts } from "../alerts/data";
import { orgQueries } from "./data";

// Organization settings (ADR 0062, mock S2): every section in one grouped
// column inside the settings area, with counts and a mark where something
// needs looking at; the section's own heading and actions over its page.
// The org's sidebar stays as it is. The platform sets defaults and limits
// (the console); everything here is the org's own.

type Section = {
  to:
    | "/$org/settings/general"
    | "/$org/settings/variables"
    | "/$org/settings/members"
    | "/$org/settings/groups"
    | "/$org/settings/roles"
    | "/$org/settings/tokens"
    | "/$org/settings/registries"
    | "/$org/settings/github"
    | "/$org/settings/cloudflare"
    | "/$org/settings/data-access"
    | "/$org/settings/analytics"
    | "/$org/settings/alerts"
    | "/$org/settings/activity";
  label: string;
  /** Only admins can use it; hidden from everyone else. */
  admin: boolean;
};

/** The sections, in groups, in the column's order. */
export const settingsGroups: { label: string; sections: Section[] }[] = [
  {
    label: "Organization",
    sections: [
      { to: "/$org/settings/general", label: "General", admin: false },
      { to: "/$org/settings/variables", label: "Variables", admin: false },
      // The org's audit trail, here since the top bar keeps only its views.
      { to: "/$org/settings/activity", label: "Activity", admin: false },
    ],
  },
  {
    label: "People and access",
    sections: [
      { to: "/$org/settings/members", label: "Members", admin: true },
      { to: "/$org/settings/groups", label: "Groups", admin: true },
      { to: "/$org/settings/roles", label: "Roles", admin: true },
      { to: "/$org/settings/tokens", label: "Tokens", admin: true },
      { to: "/$org/settings/alerts", label: "Alerts", admin: false },
    ],
  },
  {
    label: "Connections",
    sections: [
      { to: "/$org/settings/github", label: "GitHub", admin: true },
      { to: "/$org/settings/cloudflare", label: "Cloudflare", admin: true },
      { to: "/$org/settings/registries", label: "Registries", admin: true },
    ],
  },
  {
    label: "Policies",
    sections: [
      { to: "/$org/settings/data-access", label: "Data access", admin: false },
      {
        to: "/$org/settings/analytics",
        label: "Analytics defaults",
        admin: false,
      },
    ],
  },
];

/** The section the address is on. */
function useCurrentSection(): Section | undefined {
  const leaf = useLocation({ select: (l) => l.pathname.split("/").pop() });
  return settingsGroups
    .flatMap((g) => g.sections)
    .find((s) => s.to.endsWith(`/${leaf}`));
}

/**
 * A settings page's heading: its section's name, and the section's own
 * actions on the right. The column and the page bar are the layout's
 * (routes/_app.$org.settings.tsx).
 */
export function OrgSettingsBar({ actions }: { actions?: ReactNode }) {
  const current = useCurrentSection();
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 pt-6 sm:px-7">
      <h2 className="text-lg font-semibold">
        {current?.label ?? "Organization settings"}
      </h2>
      {actions && (
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {actions}
        </div>
      )}
    </div>
  );
}

/**
 * The column of sections, grouped, with counts (members, tokens) and a
 * mark on a connection that is failing. Admin-only sections are hidden
 * from everyone else, and their counts aren't loaded for them.
 */
export function OrgSettingsColumn() {
  const org = useOrg();
  const admin = useIsAdmin();
  const members = useQuery({ ...orgQueries.members(), enabled: admin }).data;
  const tokens = useQuery({ ...queries.tokens(), enabled: admin }).data;
  const alerts = useOrgOpenAlerts();
  const failing = (kind: string) =>
    alerts.some(
      (a) =>
        a.kind === "connection_failed" &&
        a.target?.connection?.startsWith(`${kind}:`),
    );
  const marks: Partial<Record<Section["to"], ReactNode>> = {
    "/$org/settings/members": members?.length,
    "/$org/settings/tokens": tokens?.filter((t) => !t.revoked_at).length,
    "/$org/settings/cloudflare": failing("cloudflare") ? "!" : undefined,
  };
  return (
    <nav
      aria-label="Organization settings"
      className="flex gap-1 overflow-x-auto border-b border-line px-3 py-2 md:sticky md:top-[calc(var(--shell-top)+64px)] md:block md:max-h-[calc(100vh-var(--shell-top)-64px)] md:w-[220px] md:shrink-0 md:overflow-y-auto md:border-r md:border-b-0 md:py-4"
    >
      {settingsGroups.map((g) => {
        const shown = g.sections.filter((s) => admin || !s.admin);
        if (shown.length === 0) return null;
        return (
          <div key={g.label} className="flex gap-1 md:mb-3 md:block">
            <div className="hidden px-2 pb-1 text-xs text-faint md:block">
              {g.label}
            </div>
            {shown.map((s) => {
              const mark = marks[s.to];
              return (
                <Link
                  key={s.to}
                  to={s.to}
                  params={{ org }}
                  activeProps={{ "aria-current": "page" }}
                  className="nav-row whitespace-nowrap"
                >
                  <span className="flex-1">{s.label}</span>
                  {mark === "!" ? (
                    <span className="font-mono text-xs text-warn">!</span>
                  ) : mark ? (
                    <span className="font-mono text-xs text-faint">{mark}</span>
                  ) : null}
                </Link>
              );
            })}
          </div>
        );
      })}
    </nav>
  );
}
