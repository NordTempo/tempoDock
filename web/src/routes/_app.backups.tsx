import { createFileRoute } from "@tanstack/react-router";
import { useIsAdmin } from "../data/queries";
import { BackupsPage } from "../features/cpbackup/BackupsPage";
import { Page, TitleRow } from "../ui/kit";

// The install's backups in the platform console (ADR 0019, 0021): the
// control plane's own backup and where every backup goes. Admins only.
// No loader: loaders stay in the initial bundle, so the page fetches its
// settings itself (it suspends until they are in).
export const Route = createFileRoute("/_app/backups")({
  component: Backups,
});

function Backups() {
  if (!useIsAdmin()) {
    return (
      <Page>
        <TitleRow title="Backups" />
        <p className="text-mut">
          Only admins manage backups.{" "}
          <span className="font-mono">dock backup status</span> shows where they
          go.
        </p>
      </Page>
    );
  }
  return <BackupsPage />;
}
