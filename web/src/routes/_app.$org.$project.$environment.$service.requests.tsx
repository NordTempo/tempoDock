import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { useOrg } from "../shell/location";

// A service's Requests tab: what reached it, in views under one tab.
// Traffic (the index: counts and endpoints from Caddy, ADR 0033) and
// Visitors (its site's analytics, ADR 0037); tracing adds Traces
// (ADR 0075). Each view is its own route, so it has its own URL.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests",
)({
  component: Requests,
});

const views = [
  ["Traffic", "/$org/$project/$environment/$service/requests"],
  ["Visitors", "/$org/$project/$environment/$service/requests/visitors"],
] as const;

function Requests() {
  const org = useOrg();
  const ref = servicePath(Route.useParams());
  return (
    <>
      <nav aria-label="Requests" className="seg self-start">
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
      <Outlet />
    </>
  );
}
