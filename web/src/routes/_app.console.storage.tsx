import { createFileRoute } from "@tanstack/react-router";
import { ConsoleTabs } from "../features/console/ConsoleTabs";
import { storageQuery } from "../features/storage/data";
import { StoragePage } from "../features/storage/StoragePage";

// Storage connections: the S3-compatible buckets Dock keeps data in.
export const Route = createFileRoute("/_app/console/storage")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(storageQuery()),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <StoragePage />
    </ConsoleTabs>
  );
}
