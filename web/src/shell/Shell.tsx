import { Link, Outlet } from "@tanstack/react-router";
import { useLiveUpdates } from "../data/live";
import { Icon } from "../ui/icons";
import { CommandLine } from "./CommandLine";
import { PaletteProvider } from "./palette";
import { Sidebar, useShellMode } from "./Sidebar";

/**
 * Everything behind login: the sidebar, the page (which brings its own
 * PageBar), and the command line at the bottom. The platform console
 * (servers) gets an amber bar across the top.
 */
export function Shell() {
  useLiveUpdates();
  const mode = useShellMode();
  return (
    <PaletteProvider>
      <a
        href="#main"
        className="sr-only z-50 rounded-ctl bg-fg px-3 py-1.5 text-side focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      {mode === "console" && <ConsoleBar />}
      <div className="flex min-h-screen flex-col lg:flex-row">
        <Sidebar mode={mode} />
        <main id="main" className="flex min-w-0 flex-1 flex-col">
          <div className="flex flex-1 flex-col">
            <Outlet />
          </div>
          <CommandLine />
        </main>
      </div>
    </PaletteProvider>
  );
}

/** The console's reminder that changes here reach every project. */
function ConsoleBar() {
  return (
    <div className="sticky top-0 z-30 flex min-h-8 flex-wrap items-center gap-x-2.5 gap-y-1 border-b border-warn-edge bg-warn-bg px-4 py-1.5 text-xs">
      <Icon name="console" size={13} className="text-warn" />
      <span className="font-semibold text-warn">Platform console</span>
      <span className="text-warn-text">
        Changes here affect every project on this Dock.
      </span>
      <Link to="/" className="ml-auto text-warn-text underline">
        Back to projects
      </Link>
    </div>
  );
}
