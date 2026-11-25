import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useState } from "react";
import { servicePath } from "../data/queries";
import { RequestsToolsSlot } from "../features/service/RequestsTools";
import { useOrg } from "../shell/location";

// A service's Requests tab: what reached it, in views under one tab.
// Traffic (the index: counts and endpoints from Caddy, ADR 0033) and
// Visitors (its site's analytics, ADR 0037); tracing adds Traces
// (ADR 0075). Each view is its own route, so it has its own URL, and
// can put its controls on the strip's row (RequestsTools).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests",
)({
  component: Requests,
});

const views = [
  ["Traffic", "/$org/$project/$environment/$service/requests"],
  ["Visitors", "/$org/$project/$environment/$service/requests/visitors"],
  ["Traces", "/$org/$project/$environment/$service/requests/traces"],
] as const;

function Requests() {
  const org = useOrg();
  const ref = servicePath(Route.useParams());
  const [tools, setTools] = useState<HTMLDivElement | null>(null);
  return (
    <RequestsToolsSlot value={tools}>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <nav aria-label="Requests" className="seg views">
          {views.map(([label, to], i) => (
            <Link
              key={to}
              to={to}
              params={{ org, ...ref }}
              activeOptions={{ exact: i === 0, includeSearch: false }}
            >
              {label}
            </Link>
          ))}
        </nav>
        <div ref={setTools} className="flex items-center gap-4 text-xs" />
      </div>
      <Outlet />
    </RequestsToolsSlot>
  );
}
