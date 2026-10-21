import { Outlet } from "@tanstack/react-router";
import { useLiveUpdates } from "../data/live";
import { CommandLine } from "./CommandLine";
import { PaletteProvider } from "./palette";
import { TopBar } from "./TopBar";

/** Everything behind login: top bar, the page, the command line. */
export function Shell() {
  useLiveUpdates();
  return (
    <PaletteProvider>
      <div className="flex min-h-screen flex-col">
        <TopBar />
        <div className="flex flex-1 flex-col">
          <Outlet />
        </div>
        <CommandLine />
      </div>
    </PaletteProvider>
  );
}
