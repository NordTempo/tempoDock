import { createFileRoute, redirect } from "@tanstack/react-router";

// The older addresses under Status pages (a page, an incident): the same
// path under /status, with its search and hash.
export const Route = createFileRoute("/_app/$org/status-pages/$")({
  beforeLoad: ({ params, location }) => {
    throw redirect({
      href: `/${params.org}/status/${params._splat ?? ""}${location.searchStr}${location.hash ? `#${location.hash}` : ""}`,
      replace: true,
    });
  },
});
