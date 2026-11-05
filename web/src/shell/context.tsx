import { Link } from "@tanstack/react-router";
import type { components } from "../api/schema";
import {
  useIsOperator,
  useProjectOverview,
  useProjectTree,
  useServers,
} from "../data/queries";
import type { ServiceView } from "../data/types";
import { statusTitle } from "../features/servers/parts";
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
 * "● production ▾": the environment in view, and a menu of the others. On
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
    <span className="flex items-center gap-[7px] rounded-full border border-edge bg-panel px-[9px] py-1 text-xs hover:border-dash">
      <span className={toneText[envTone(here?.services ?? [])]}>
        <span className="dot" />
      </span>
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
        {(close) =>
          others.map((e) => {
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
              >
                {dot}
                <span className="flex-1">
                  {e.environment.name} <LockMark lock={e.environment.lock} />
                </span>
                <PrMark preview={e.environment.preview} />
                <span className="font-mono text-2xs text-faint">
                  {twin.service.active_release
                    ? `r${twin.service.active_release}`
                    : "none"}
                </span>
              </Link>
            ) : (
              <Link
                key={e.environment.id}
                to="/$org/$project/settings/variables/$environment"
                params={{ org, project, environment: e.environment.name }}
                onClick={close}
                className={menuItem}
              >
                {dot}
                <span className="flex-1">
                  {e.environment.name} <LockMark lock={e.environment.lock} />
                </span>
                <PrMark preview={e.environment.preview} />
              </Link>
            );
          })
        }
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
 * ● per server in view: the environment's server, or every server the
 * project runs on. Anything but online says so in words: "✕ homelab
 * offline".
 */
export function ServerDots({
  project,
  environment,
}: {
  project: string;
  environment?: string;
}) {
  const servers = useServers();
  const tree = useProjectTree();
  const operator = useIsOperator();
  const environments =
    tree.find((p) => p.project.name === project)?.environments ?? [];
  const names = new Set(
    environments
      .filter((e) => !environment || e.environment.name === environment)
      .map((e) => e.environment.server),
  );
  const shown = servers.filter((s) => names.has(s.name));
  return (
    <span className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-faint">
      {shown.map((s) => {
        const state = serverState(s.status);
        const label = (
          <>
            <span className={toneText[state.tone]}>
              <Glyph char={state.glyph ?? "●"} />
            </span>
            <span className="font-mono">{s.name}</span>
            {s.status !== "online" && (
              <span className={toneText[state.tone]}>{state.label}</span>
            )}
          </>
        );
        // Server pages are in the console, for operators.
        return operator ? (
          <Link
            key={s.id}
            to="/console/servers/$server"
            params={{ server: s.name }}
            className="flex items-center gap-1.5 hover:text-fg"
            title={statusTitle(s)}
          >
            {label}
          </Link>
        ) : (
          <span
            key={s.id}
            className="flex items-center gap-1.5"
            title={statusTitle(s)}
          >
            {label}
          </span>
        );
      })}
    </span>
  );
}
