import { useQuery } from "@tanstack/react-query";
import { createFileRoute, useLocation } from "@tanstack/react-router";
import { useEffect } from "react";
import { api, body } from "../api/client";
import { Markdown } from "../features/help/Markdown";
import { ErrorText } from "../ui/kit";

// A page of the user guide (docs/guide), served by the daemon it
// describes, so it matches this version. Help topics' "Guide" links
// open it at a heading.
export const Route = createFileRoute("/_app/$org/help/guide/$page")({
  component: GuidePage,
});

function GuidePage() {
  const { page } = Route.useParams();
  const hash = useLocation({ select: (l) => l.hash });
  const guide = useQuery({
    queryKey: ["guide", page],
    queryFn: () =>
      body(api.GET("/api/v1/guide/{page}", { params: { path: { page } } })),
    // The guide changes only with the daemon's version.
    staleTime: Number.POSITIVE_INFINITY,
  });
  // The heading a link points at exists once the page has loaded.
  useEffect(() => {
    if (guide.data && hash) document.getElementById(hash)?.scrollIntoView();
  }, [guide.data, hash]);
  if (guide.error) return <ErrorText error={guide.error} />;
  if (!guide.data) return <p className="text-mut">Loading…</p>;
  return (
    <div className="max-w-[760px]">
      <Markdown source={guide.data.markdown} page={page} />
    </div>
  );
}
