import { Link } from "@tanstack/react-router";
import { useProjectTree, useSeesPlatform } from "../data/queries";
import type { ServerStatus } from "../data/types";
import { Glyph } from "../ui/kit";
import { serverState, type Tone, toneText } from "../ui/status";

// What a PageBar shows next to the title inside a project: the
// environment switch's pieces and the servers in view.

/**
 * One button of an environment switch, the row of buttons both the
 * project header and a service's header show. The one in view carries
 * aria-current ("true" on a button, "page" on a link).
 */
export const envSegment =
  "inline-flex min-h-[26px] items-center gap-1.5 whitespace-nowrap px-2.5 text-ctl text-mut no-underline hover:text-fg aria-[current=page]:bg-sel aria-[current=page]:text-fg aria-[current=true]:bg-sel aria-[current=true]:text-fg";

/**
 * The box around an environment switch's buttons. On a narrow screen
 * they wrap to more lines instead of running off it.
 */
export const envSwitchBox =
  "inline-flex max-w-full flex-wrap overflow-hidden rounded-ctl border border-edge";

/**
 * A small dot in a problem's colour on an environment's button, named
 * for screen readers.
 */
export function ToneDot({ tone }: { tone: Tone | undefined }) {
  if (!tone) return null;
  return (
    <span
      role="img"
      aria-label={tone === "fail" ? ", has a problem" : ", needs a look"}
      className={`dot ${toneText[tone]}`}
    />
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
