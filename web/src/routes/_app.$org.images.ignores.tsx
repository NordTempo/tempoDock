import { createFileRoute, Link } from "@tanstack/react-router";
import { imageQueries, useIgnores } from "../features/images/data";
import { IgnoresTable } from "../features/images/IgnoresTable";
import { useOrg } from "../shell/location";
import { Page, PageBar } from "../ui/kit";

// Every vulnerability ignore in the org (ADR 0074, mock L1): one place
// to review the exceptions. They are made from an image's findings.
export const Route = createFileRoute("/_app/$org/images/ignores")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(imageQueries.ignores()),
  component: Ignores,
});

function Ignores() {
  const org = useOrg();
  const ignores = useIgnores();
  return (
    <>
      <PageBar
        parent={
          <Link
            to="/$org/images"
            params={{ org }}
            className="-my-[3px] inline-flex min-h-[24px] items-center hover:text-fg"
          >
            Images
          </Link>
        }
        title="Ignores"
      />
      <Page gap="gap-3">
        {ignores.length === 0 ? (
          <p className="text-mut">
            None. Ignore a vulnerability from an image's findings, or with{" "}
            <span className="font-mono">dock vulns ignore</span>.
          </p>
        ) : (
          <>
            <IgnoresTable ignores={ignores} />
            <p className="text-xs text-mut">
              Expired ignores no longer apply and go after 30 days.
            </p>
          </>
        )}
        <p className="font-mono text-2xs text-mut">dock vulns ignores</p>
      </Page>
    </>
  );
}
