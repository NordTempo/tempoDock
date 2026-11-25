import { Link, useMatchRoute, useNavigate } from "@tanstack/react-router";
import { Fragment, type ReactNode } from "react";
import { useCurrentContext, useOrg } from "../../shell/location";
import { notify } from "../../ui/notice";
import { orgPlaces, type Place, projectPlaces, servicePlaces } from "./places";
import { type Do, groups, type TopicId, topicIds, topics } from "./topics";

type Context = ReturnType<typeof useCurrentContext>;

/** A topic's command with the page's service, environment and project in it. */
export function fillCli(cli: string, here: Context) {
  return cli
    .replaceAll("{service}", here.service ?? "<service>")
    .replaceAll("{env}", here.environment ?? "<environment>")
    .replaceAll("{project}", here.project ?? "<project>");
}

/** "deploying#rollback" as the guide route's page and anchor. */
export function guideTarget(guide: string) {
  const [page = "README", hash] = guide.split("#");
  return { page, hash };
}

/**
 * One topic: what it is, what to do (commands filled in for the page),
 * what to watch out for, related topics and the guide. The help panel
 * and the help page both show it; `topicLink` renders a related topic
 * the way its host opens one, `onGo` runs after a row's link moved the
 * page, and `onGuide` when the Guide link is followed.
 */
export function TopicBody({
  id,
  topicLink,
  onGo,
  onGuide,
}: {
  id: TopicId;
  topicLink: (id: TopicId, label: string) => ReactNode;
  onGo?: () => void;
  onGuide?: () => void;
}) {
  const topic = topics[id];
  const here = useCurrentContext();
  const org = useOrg();
  const guide = guideTarget(topic.guide);
  return (
    <article aria-labelledby={`help-${id}`} className="flex flex-col gap-4">
      <div>
        <h2 id={`help-${id}`} className="text-md font-semibold">
          {topic.title}
        </h2>
        <p className="mt-0.5 text-sec">{topic.what}</p>
      </div>
      <ul className="flex flex-col">
        {topic.do.map((row) => (
          <DoRow key={row.what} row={row} here={here} onGo={onGo} />
        ))}
      </ul>
      {topic.watch.length > 0 && (
        <section aria-label="Watch out" className="flex flex-col gap-1">
          <h3 className="text-2xs font-medium text-mut">Watch out</h3>
          <ul className="flex flex-col gap-1 text-sec">
            {topic.watch.map((line) => (
              <li key={line} className="flex gap-2">
                <span aria-hidden className="text-warn">
                  !
                </span>
                <span>{line}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      <section
        aria-label="More"
        className="flex flex-wrap items-baseline gap-x-3 gap-y-1"
      >
        <h3 className="text-2xs font-medium text-mut">Related</h3>
        {topic.related.map((r) => (
          <span key={r}>{topicLink(r, topics[r].title)}</span>
        ))}
        <span className="flex-1" />
        <Link
          to="/$org/help/guide/$page"
          params={{ org, page: guide.page }}
          hash={guide.hash}
          onClick={onGuide}
          className={helpLink}
        >
          Guide
        </Link>
      </section>
    </article>
  );
}

/** A thing to do: its words, its command to copy, and its screen when there is one here. */
function DoRow({
  row,
  here,
  onGo,
}: {
  row: Do;
  here: Context;
  onGo?: () => void;
}) {
  const cli = row.cli && fillCli(row.cli, here);
  return (
    <li className="flex items-start gap-3 border-b border-soft py-1.5 first:border-t">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="text-sec">{row.what}</span>
        {cli && (
          <button
            type="button"
            title="Copy"
            aria-label={`Copy: ${cli}`}
            className="self-start text-left font-mono text-xs text-fg hover:text-info"
            onClick={() =>
              navigator.clipboard
                .writeText(cli)
                .then(() => notify("Command copied"))
            }
          >
            <CliWords cli={cli} />
          </button>
        )}
      </div>
      {row.at && <PlaceLink at={row.at} here={here} onGo={onGo} />}
    </li>
  );
}

/**
 * A link to a row's screen, when the page has what it needs (a service
 * for a tab) and isn't that screen already.
 */
function PlaceLink({
  at,
  here,
  onGo,
}: {
  at: Place;
  here: Context;
  onGo?: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const matchRoute = useMatchRoute();
  const go = (move: Promise<void>) => void move.then(() => onGo?.());
  const className = "lnk shrink-0 text-xs";
  if ("s" in at) {
    const { project, environment, service } = here;
    if (!project || !environment || !service) return null;
    const place: { to: string; label: string; hash?: string } =
      servicePlaces[at.s];
    const params = { org, project, environment, service };
    // A section's link stays on its own page: it scrolls there.
    if (!place.hash && matchRoute({ to: servicePlaces[at.s].to, params }))
      return null;
    return (
      <button
        type="button"
        className={className}
        onClick={() =>
          go(navigate({ to: servicePlaces[at.s].to, params, hash: place.hash }))
        }
      >
        {place.label}
      </button>
    );
  }
  if ("p" in at) {
    const { project } = here;
    if (!project) return null;
    const place: { to: string; label: string; hash?: string } =
      projectPlaces[at.p];
    const to = projectPlaces[at.p].to;
    if (!place.hash && matchRoute({ to, params: { org, project } }))
      return null;
    return (
      <button
        type="button"
        className={className}
        onClick={() =>
          go(navigate({ to, params: { org, project }, hash: place.hash }))
        }
      >
        {place.label}
      </button>
    );
  }
  if ("o" in at) {
    const place = orgPlaces[at.o];
    if (matchRoute({ to: place.to, params: { org } })) return null;
    return (
      <button
        type="button"
        className={className}
        onClick={() => go(navigate({ to: place.to, params: { org } }))}
      >
        {place.label}
      </button>
    );
  }
  const [to, label] =
    "account" in at
      ? (["/account", "Your account"] as const)
      : (["/console/servers", "Servers"] as const);
  return (
    <button
      type="button"
      className={className}
      onClick={() => go(navigate({ to }))}
    >
      {label}
    </button>
  );
}

/**
 * A command that wraps only between its words, so a flag (--memory)
 * stays whole; a word wider than the line still breaks inside.
 */
function CliWords({ cli }: { cli: string }) {
  let at = 0;
  const words = cli.split(" ").map((word) => {
    const w = { word, at };
    at += word.length + 1;
    return w;
  });
  return words.map(({ word, at }) => (
    <Fragment key={at}>
      {at > 0 && " "}
      <span className="inline-block max-w-full [overflow-wrap:anywhere]">
        {word}
      </span>
    </Fragment>
  ));
}

/**
 * A related topic or the guide, a plain small link in the panel and on
 * the help page alike (not `lnk`, which is a bordered button on pages).
 */
export const helpLink =
  "inline-flex min-h-[24px] min-w-[24px] items-center text-xs text-mut underline-offset-3 hover:text-fg hover:underline";

/** A topic in TopicList: the current one marked by aria-current. */
export const topicItem =
  "flex min-h-[24px] w-full items-center rounded-ctl px-2 text-left text-sec hover:bg-hover hover:text-fg aria-[current]:bg-sel aria-[current]:text-fg";

/**
 * Every topic in its groups, operator-only ones for operators; `item`
 * renders one the way its host opens it (a button in the panel, a link
 * on the help page).
 */
export function TopicList({
  operator,
  item,
}: {
  operator: boolean;
  item: (id: TopicId) => ReactNode;
}) {
  return (
    <nav aria-label="Help topics" className="flex flex-col gap-3">
      {groups.map((group) => (
        <section key={group} aria-label={group}>
          <h3 className="mb-0.5 px-2 text-2xs font-medium text-mut">{group}</h3>
          {topicIds
            .filter(
              (id) =>
                topics[id].group === group &&
                (operator || !topics[id].operator),
            )
            .map((id) => (
              <div key={id}>{item(id)}</div>
            ))}
        </section>
      ))}
    </nav>
  );
}
