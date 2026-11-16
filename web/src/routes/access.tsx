import { useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { useEffect } from "react";
import { api, body } from "../api/client";
import { useLogout } from "../data/mutations";
import { queries, useSession } from "../data/queries";
import { ErrorText, Prop } from "../ui/kit";

// Where a visitor of a login-protected service lands (ADR 0030): its
// server's access check sends them here with the hostname and the path
// they asked for. Signed out, they log in first and come back; signed in,
// the page gets a one-minute grant and goes on to the service by itself.
// That is safe without a click: the grant only works on that hostname.
export const Route = createFileRoute("/access")({
  validateSearch: (search: Record<string, unknown>) => ({
    host: typeof search.host === "string" ? search.host : "",
    to: typeof search.to === "string" ? search.to : "/",
  }),
  beforeLoad: async ({ context, location }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: Access,
});

function Access() {
  const { host, to } = Route.useSearch();
  const session = useSession();
  const logout = useLogout();
  const grant = useQuery({
    queryKey: ["access-grant", host, to],
    queryFn: () =>
      body(api.POST("/api/v1/access/grants", { body: { hostname: host, to } })),
    enabled: host !== "",
    retry: false,
    // A grant lasts a minute; never reuse one.
    gcTime: 0,
  });
  const url = grant.data?.url;
  useEffect(() => {
    if (url) window.location.replace(url);
  }, [url]);

  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[420px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold break-all">
            {host ? `Opening ${host}` : "Nothing to open"}
          </h1>
          <span className="font-mono text-xs text-faint">
            {window.location.host}
          </span>
        </div>
        {grant.data && (
          <dl className="grid grid-cols-[max-content_1fr] gap-x-5 gap-y-1.5 [&>dt]:text-mut">
            <Prop label="Service">
              <span className="font-mono">
                {grant.data.project} / {grant.data.environment} /{" "}
                {grant.data.service}
              </span>
            </Prop>
            <Prop label="As">{session?.name}</Prop>
          </dl>
        )}
        {!host ? (
          <p className="text-mut">
            This page is where a protected service sends you to sign in. Open
            the service's address instead.
          </p>
        ) : grant.isError ? (
          <>
            <p>
              <span className="text-fail">✕</span>{" "}
              <ErrorText error={grant.error} />
            </p>
            <p className="text-xs text-faint">
              Signed in as {session?.name}.{" "}
              <button
                type="button"
                className="text-info"
                disabled={logout.isPending}
                onClick={() => logout.mutate()}
              >
                Log out
              </button>{" "}
              to use another account.
            </p>
          </>
        ) : (
          <p className="text-mut">Continuing…</p>
        )}
      </main>
    </div>
  );
}
