import { Link } from "@tanstack/react-router";
import type { components } from "../api/schema";
import {
  useProjectOverview,
  useProjectTree,
  useSeesPlatform,
} from "../data/queries";
import type { ServerStatus, ServiceView } from "../data/types";
import { Icon } from "../ui/icons";
import { Glyph, LockMark, Menu, menuItem } from "../ui/kit";
import { serverState, serviceState, type Tone, toneText } from "../ui/status";
import { useOrg } from "./location";

// What a PageBar shows next to the title inside a project: the environment
// switch and the servers in view.

type PullRequest = components["schemas"]["PullRequestRef"];

const worst: Tone[] = ["fail", "warn", "prog", "ok", "mut"];

/** The colour of an environment's dot: its worst service. */
function envTone(services: ServiceView[]): Tone {
  const tones = new Set(services.map((s) => serviceState(s).tone));
  return worst.find((t) => tones.has(t)) ?? "mut";
}

/** "PR #12": marks a preview environment (ADR 0056) in lists. */
export function PrMark({ preview }: { preview: PullRequest | undefined }) {
  if (!preview) return null;
  return (
    <span className="font-mono text-2xs text-info" title={preview.title}>
      PR #{preview.number}
    </span>
  );
}

/**
 * "● production ▾": the environment in view (the dot only off a service
 * page), and a menu of the others. On
 * a service page it lists the environments that have the same service and
 * keeps you on it; elsewhere it goes to each environment's variables.
 * Preview environments come last, marked with their pull request; on one,
 * its pull request is linked next to the switch.
 */
export function EnvSwitch({
  project,
  environment,
  service,
}: {
  project: string;
  environment: string;
  service?: string;
}) {
  const org = useOrg();
  const { environments } = useProjectOverview(project);
  const here = environments.find((e) => e.environment.name === environment);
  const others = environments.filter(
    (e) =>
      e.environment.name !== environment &&
      (!service || e.services.some((s) => s.service.name === service)),
  );
  const pr = here?.environment.preview;
  const pill = (
    <span className="flex items-center gap-[7px] rounded-ctl border border-edge px-[9px] py-1 text-xs hover:border-dash hover:bg-hover">
      {/* On a service page the service's own chip says its state; an
          environment-wide dot beside it would read as the service's. */}
      {!service && (
        <span className={toneText[envTone(here?.services ?? [])]}>
          <span className="dot" />
        </span>
      )}
      {environment}
      <LockMark lock={here?.environment.lock} />
      {others.length > 0 && (
        <Icon name="chevronDown" size={11} className="text-mut" />
      )}
    </span>
  );
  const pull = pr && (
    <a
      href={pr.url}
      target="_blank"
      rel="noreferrer"
      title={`${pr.repo}#${pr.number}: ${pr.title} by ${pr.author}`}
      className="max-w-[40ch] truncate text-xs text-mut hover:text-fg"
    >
      <span className="font-mono text-info">PR #{pr.number}</span> {pr.title}
    </a>
  );
  const control =
    others.length === 0 ? (
      pill
    ) : (
      <Menu label={`Environment: ${environment}`} button={pill}>
        {(close) => [
          // The one in view first, checked, so the menu reads as a choice.
          // Environments are a choice of one (menuitemradio), so the menu's
          // children are all items, as role="menu" requires.
          <div
            key="here"
            role="menuitemradio"
            aria-checked="true"
            tabIndex={-1}
            className={`${menuItem} cursor-default text-fg hover:bg-transparent`}
          >
            <span aria-hidden className="w-[7px] text-2xs">
              ✓
            </span>
            <span className="flex-1">
              {environment} <LockMark lock={here?.environment.lock} />
            </span>
          </div>,
          ...others.map((e) => {
            const twin = e.services.find((s) => s.service.name === service);
            const dot = (
              <span
                className={
                  toneText[twin ? serviceState(twin).tone : envTone(e.services)]
                }
              >
                <span className="dot" />
              </span>
            );
            return service && twin ? (
              <Link
                key={e.environment.id}
                to="/$org/$project/$environment/$service"
                params={{
                  org,
                  project,
                  environment: e.environment.name,
                  service,
                }}
                onClick={close}
                className={menuItem}
                role="menuitemradio"
                aria-checked="false"
              >
                {dot}
                <span className="flex-1">
                  {e.environment.name} <LockMark lock={e.environment.lock} />
                </span>
                <PrMark preview={e.environment.preview} />
                <span className="font-mono text-2xs text-faint">
                  {twin.service.active_release
                    ? `r${twin.service.active_release}`
                    : "not deployed"}
                </span>
              </Link>
            ) : (
              <Link
                key={e.environment.id}
                to="/$org/$project/$environment"
                params={{ org, project, environment: e.environment.name }}
                onClick={close}
                className={menuItem}
                role="menuitemradio"
                aria-checked="false"
              >
                {dot}
                <span className="flex-1">
                  {e.environment.name} <LockMark lock={e.environment.lock} />
                </span>
                <PrMark preview={e.environment.preview} />
              </Link>
            );
          }),
          <hr key="line" className="my-1 border-0 border-t border-line" />,
          <Link
            key="compare"
            to="/$org/$project/compare"
            params={{ org, project }}
            onClick={close}
            className={menuItem}
            role="menuitem"
          >
            <span aria-hidden className="w-[7px]" />
            <span className="flex-1">Compare environments</span>
          </Link>,
        ]}
      </Menu>
    );
  return (
    <>
      {control}
      {pull}
    </>
  );
}

/**
 * The servers of a project's environments that aren't online, in words:
 * "✕ homelab offline". Only operators see server names (and get a link
 * to its page); everyone else sees "✕ server offline".
 */
export function ServerDots({ project }: { project: string }) {
  const tree = useProjectTree();
  const operator = useSeesPlatform();
  const environments =
    tree.find((p) => p.project.name === project)?.environments ?? [];
  const shown = new Map<string, ServerStatus>();
  for (const { environment: e } of environments) {
    if (e.server_status !== "online")
      shown.set(e.server ?? "", e.server_status);
  }
  if (shown.size === 0) return null;
  return (
    <span className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-faint">
      {[...shown].map(([name, status]) => {
        const state = serverState(status);
        const label = (
          <>
            <span className={toneText[state.tone]}>
              <Glyph char={state.glyph ?? "●"} />
            </span>
            <span className={name ? "font-mono" : undefined}>
              {name || "server"}
            </span>
            <span className={toneText[state.tone]}>{state.label}</span>
          </>
        );
        const title = `${name ? `Server ${name}` : "The server"} is ${state.label}`;
        // Server pages are in the console, for operators.
        return operator && name ? (
          <Link
            key={name}
            to="/console/servers/$server"
            params={{ server: name }}
            className="flex items-center gap-1.5 hover:text-fg"
            title={title}
          >
            {label}
          </Link>
        ) : (
          <span key={name} className="flex items-center gap-1.5" title={title}>
            {label}
          </span>
        );
      })}
    </span>
  );
}
