import { createFileRoute } from "@tanstack/react-router";
import { AddressPage } from "../features/address/AddressPage";
import { addressQuery } from "../features/address/data";
import { ConsoleTabs } from "../features/console/ConsoleTabs";

// Dock's own address: the public hostname it is served at (ADR 0029).
export const Route = createFileRoute("/_app/console/address")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(addressQuery()),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <AddressPage />
    </ConsoleTabs>
  );
}
