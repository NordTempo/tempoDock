import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { servicePath, useService } from "../data/queries";
import { useOrg } from "../shell/location";

// A service's Settings tab (ADR 0076): what configures it, in views under
// one tab, each its own route. Build & run (the index: source, builder,
// commands, health checks, limits, volumes), Domains, and for a stateful
// service its Backups (restore points).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings",
)({
  component: Settings,
});

function Settings() {
  const org = useOrg();
  const ref = servicePath(Route.useParams());
  const { service } = useService(ref);
  const views = [
    ["Build & run", "/$org/$project/$environment/$service/settings"],
    ["Domains", "/$org/$project/$environment/$service/settings/domains"],
    ...(service.stateful
      ? ([
          ["Backups", "/$org/$project/$environment/$service/settings/backups"],
        ] as const)
      : []),
  ] as const;
  return (
    <>
      <nav aria-label="Settings" className="seg self-start">
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
