import {
  createFileRoute,
  Link,
  Outlet,
  useMatch,
} from "@tanstack/react-router";
import { useIsOperator } from "../data/queries";
import { TopicList, topicItem } from "../features/help/Topic";
import { topics } from "../features/help/topics";
import { useOrg } from "../shell/location";
import { PageBar } from "../ui/kit";

// Help as pages (ADR 0076's Help): every topic in a column beside the
// open topic or guide page. The ? panel (features/help) is the usual way
// in; these give a topic or a guide page its own address.
export const Route = createFileRoute("/_app/$org/help")({
  component: HelpPages,
});

function HelpPages() {
  const org = useOrg();
  const operator = useIsOperator();
  // Every guide page is under The full guide, so it stays marked there.
  const inGuide = !!useMatch({
    from: "/_app/$org/help/guide/$page",
    shouldThrow: false,
  });
  return (
    <>
      <PageBar title="Help" />
      {/* Narrow windows: the open topic first, the list after it. */}
      <div className="flex w-full max-w-[1400px] flex-col-reverse md:flex-row">
        <div className="border-b border-line px-3 py-3 md:sticky md:top-[calc(var(--shell-top)+64px)] md:max-h-[calc(100vh-var(--shell-top)-64px)] md:w-[220px] md:shrink-0 md:overflow-y-auto md:border-r md:border-b-0 md:py-4">
          <TopicList
            operator={operator}
            item={(id) => (
              <Link
                to="/$org/help/$topic"
                params={{ org, topic: id }}
                className={topicItem}
                activeProps={{ "aria-current": "true" }}
              >
                {topics[id].title}
              </Link>
            )}
          />
          <Link
            to="/$org/help/guide/$page"
            params={{ org, page: "README" }}
            className={`${topicItem} mt-3`}
            aria-current={inGuide ? "true" : undefined}
          >
            The full guide
          </Link>
        </div>
        <div className="min-w-0 flex-1 px-4 pt-6 pb-10 sm:px-7">
          <Outlet />
        </div>
      </div>
    </>
  );
}
