import { Link, useLocation } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { consoleEntryOf } from "./nav";

/**
 * A console page that belongs to a sidebar entry with several pages
 * (Backups and storage, People and access, Connections): the page, with
 * a row of its entry's pages under its title bar, so the others are one
 * click away and the sidebar stays short. Wraps the page component in its
 * route; the page keeps its own bar, which the wrapper puts first.
 */
export function ConsoleTabs({ children }: { children: ReactNode }) {
  const pathname = useLocation({ select: (l) => l.pathname });
  const entry = consoleEntryOf(pathname);
  return (
    // The page's own bar (a <header>) goes first, then this row, then the page.
    <div className="flex min-w-0 flex-1 flex-col [&>header]:-order-1">
      {entry && entry.pages.length > 1 && (
        <nav
          aria-label={entry.label}
          className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap sm:px-7"
        >
          {entry.pages.map((p) => (
            <Link
              key={p.to}
              to={p.to}
              activeOptions={{ exact: true, includeSearch: false }}
            >
              {p.label}
            </Link>
          ))}
        </nav>
      )}
      {children}
    </div>
  );
}
