import { Outlet } from "@tanstack/react-router";
import { useLiveUpdates } from "../data/live";
import { PaletteProvider } from "./palette";
import { Sidebar } from "./Sidebar";

export function Shell() {
  useLiveUpdates();
  return (
    <PaletteProvider>
      <div className="grid h-screen grid-cols-[232px_minmax(0,1fr)]">
        <Sidebar />
        <main className="overflow-auto pb-20">
          <Outlet />
        </main>
      </div>
    </PaletteProvider>
  );
}
