import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { helpLink, TopicBody } from "../features/help/Topic";
import { isTopicId } from "../features/help/topics";

// One help topic at its own address, for links and reading at full width.
// The topic is checked in the component, not in beforeLoad: route options
// other than components stay in the initial bundle, and with them every
// topic's text.
export const Route = createFileRoute("/_app/$org/help/$topic")({
  component: HelpTopic,
});

function HelpTopic() {
  const { org, topic } = Route.useParams();
  if (!isTopicId(topic)) throw notFound({ routeId: "/_app/$org" });
  return (
    <div className="max-w-[760px]">
      <TopicBody
        id={topic}
        topicLink={(id, label) => (
          <Link
            to="/$org/help/$topic"
            params={{ org, topic: id }}
            className={helpLink}
          >
            {label}
          </Link>
        )}
      />
    </div>
  );
}
