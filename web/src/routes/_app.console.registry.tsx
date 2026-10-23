import { createFileRoute } from "@tanstack/react-router";
import { registryQuery } from "../features/registry/data";
import { RegistryPage } from "../features/registry/RegistryPage";

// The platform registry: size, pruning and the services keeping images.
export const Route = createFileRoute("/_app/console/registry")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(registryQuery()),
  component: RegistryPage,
});
