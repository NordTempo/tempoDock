import { createFileRoute } from "@tanstack/react-router";
import { previewQueries } from "../features/previews/data";
import { PreviewsSection } from "../features/previews/PreviewsSection";
import { Page } from "../ui/kit";

// A project's preview environments settings: one per pull request
// (ADR 0056). They were on the project page before ADR 0059.
export const Route = createFileRoute("/_app/$org/$project/settings/previews")({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.prefetchQuery(previewQueries.settings(params.project)),
  component: Previews,
});

function Previews() {
  return (
    <Page>
      <PreviewsSection project={Route.useParams().project} />
    </Page>
  );
}
