import { Link, useMatchRoute } from "@tanstack/react-router";
import { useProjectOverview, useSession, whyNot } from "../../data/queries";
import type { Environment, ServiceView } from "../../data/types";
import { envSegment, envSwitchBox, ToneDot } from "../../shell/context";
import { useOrg } from "../../shell/location";
import { Icon } from "../../ui/icons";
import { LockMark, Menu, menuItem } from "../../ui/kit";
import { serviceState } from "../../ui/status";
import { serviceTabs, tabsFor } from "./tabs";

/**
 * A service's environment switch: the project header's buttons, one for
 * each environment the service is in, the one in view marked. Another
 * where the service has trouble gets a dot; previews, which can be many,
 * sit in one menu at the end. Each keeps the tab you're on where the
 * service has it, else opens its Overview. On a preview, its pull
 * request is linked next to the switch.
 */
export function EnvSwitch({
  project,
  environment,
  service,
}: {
  project: string;
  environment: string;
  service: string;
}) {
  const org = useOrg();
  const tab = useServiceTab();
  const session = useSession();
  const twins = useProjectOverview(project).environments.flatMap((e) => {
    const view = e.services.find((s) => s.service.name === service);
    return view ? [{ env: e.environment, view }] : [];
  });
  const regular = twins.filter((t) => !t.env.preview);
  const previews = twins.filter((t) => t.env.preview);
  const current = previews.find((t) => t.env.name === environment);
  const pr = twins.find((t) => t.env.name === environment)?.env.preview;

  const link = (
    { env, view }: { env: Environment; view: ServiceView },
    className: string,
    /** Set inside the previews menu: closes it, and makes the link an item. */
    close?: () => void,
  ) => {
    const here = env.name === environment;
    const { tone } = serviceState(view);
    return (
      <Link
        key={env.id}
        to={
          tabsFor(
            view,
            whyNot(session, "admin", { project, environment: env.name }) ===
              undefined,
          ).includes(tab)
            ? tab.to
            : serviceTabs[0].to
        }
        params={{ org, project, environment: env.name, service }}
        aria-current={here ? "page" : undefined}
        onClick={close}
        role={close ? "menuitem" : undefined}
        className={className}
      >
        {env.name}
        <LockMark lock={env.lock} />
        {/* The header's own chip says the state of the one in view. */}
        {!here && (tone === "fail" || tone === "warn") && (
          <ToneDot tone={tone} />
        )}
      </Link>
    );
  };

  return (
    <>
      <nav aria-label="Environment" className={envSwitchBox}>
        {regular.map((t) =>
          link(t, `${envSegment} not-first:border-l not-first:border-edge`),
        )}
        {previews.length > 0 && (
          <Menu
            label={current ? `Previews: ${current.env.name}` : "Previews"}
            className={regular.length > 0 ? "border-l border-edge" : ""}
            button={
              <span
                aria-current={current ? "page" : undefined}
                className={envSegment}
              >
                {current ? current.env.name : "previews"}
                <span className="text-faint">{previews.length}</span>
                <Icon name="chevronDown" size={12} />
              </span>
            }
          >
            {(close) => previews.map((t) => link(t, menuItem, close))}
          </Menu>
        )}
      </nav>
      {pr && (
        <a
          href={pr.url}
          target="_blank"
          rel="noreferrer"
          title={`${pr.repo}#${pr.number}: ${pr.title} by ${pr.author}`}
          className="max-w-[40ch] truncate text-xs text-mut hover:text-fg"
        >
          <span className="font-mono text-info">PR #{pr.number}</span>{" "}
          {pr.title}
        </a>
      )}
    </>
  );
}

/**
 * The service tab in view: the one whose address the page's starts
 * with, Overview when none does (Overview's is every tab's prefix).
 */
function useServiceTab() {
  const matchRoute = useMatchRoute();
  return (
    serviceTabs.find(
      (t) => t.label !== "Overview" && matchRoute({ to: t.to, fuzzy: true }),
    ) ?? serviceTabs[0]
  );
}
