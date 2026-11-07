import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { queries } from "../../data/queries";

/**
 * A line under the console bar for each server that drains (ADR 0063), on
 * every console page, so an operator who moved on keeps seeing it. A
 * paused one says why: it waits for them. Static text, patched by server
 * events; nothing repaints.
 */
export function DrainStrip() {
  const servers = useQuery(queries.servers()).data ?? [];
  const draining = servers.filter((s) => s.drain);
  if (draining.length === 0) return null;
  return (
    <div className="flex flex-col border-b border-line bg-page text-xs">
      {draining.map((s) => {
        const d = s.drain;
        if (!d) return null;
        const paused = d.status === "paused";
        return (
          <div
            key={s.id}
            className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-4 py-1.5"
          >
            <span className={paused ? "text-warn" : "text-prog"}>●</span>
            <span>
              {paused ? "Drain paused on" : "Draining"}{" "}
              <span className="font-mono">{s.name}</span>
            </span>
            <span className="text-mut">
              {d.done} of {d.total} moved
            </span>
            {paused && d.error && (
              <span className="min-w-0 flex-1 truncate text-sec">
                {d.error}
              </span>
            )}
            <Link
              to="/console/servers/$server"
              params={{ server: s.name }}
              hash="drain"
              className="ml-auto inline-flex min-h-[24px] items-center text-info underline"
            >
              Open
            </Link>
          </div>
        );
      })}
    </div>
  );
}
