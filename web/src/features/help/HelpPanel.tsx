import { Link, useRouterState } from "@tanstack/react-router";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { useSeesPlatform } from "../../data/queries";
import {
  isTyping,
  type LegendItem,
  modKey,
  useKeydown,
  useLegend,
} from "../../shell/keys";
import { useCurrentContext, useOrg } from "../../shell/location";
import { closeHelp, type HelpState, openHelp } from "./open";
import { matchTopics } from "./search";
import { fillCli, TopicBody, TopicList, topicItem } from "./Topic";
import { type TopicId, topics } from "./topics";
import { topicAt } from "./where";

/**
 * The help panel (Saile's pick B): a column at the right edge, under the
 * top bar, beside the page rather than over it, so the page stays usable.
 * It shows the topic for the page (where.ts) or one picked here, with
 * the page's keys under it; the search finds topics and their rows.
 * Esc or ? closes it. Loaded on the first ? (open.tsx).
 */
export default function HelpPanel({ pick }: { pick: HelpState["pick"] }) {
  const routeId = useRouterState({
    select: (s) => s.matches.at(-1)?.routeId ?? "",
  });
  const path = useRouterState({ select: (s) => s.location.pathname });
  const shown = pick && pick.path === path ? pick.topic : topicAt(routeId);
  const [query, setQuery] = useState("");
  const [browsing, setBrowsing] = useState(false);
  const panel = useRef<HTMLElement>(null);
  const operator = useSeesPlatform();

  // Focus moves into the panel and back to where it was when it closes.
  const [previous] = useState(() => document.activeElement);
  useEffect(() => {
    panel.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected)
        previous.focus();
    };
  }, [previous]);

  // Esc closes, unless a dialog takes it or it cancels a field on the page.
  useKeydown((event) => {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    if (document.querySelector("dialog:modal")) return;
    const inside = panel.current?.contains(document.activeElement);
    if (!inside && isTyping(event)) return;
    closeHelp();
  });

  const show = (id: TopicId) => {
    setQuery("");
    setBrowsing(false);
    openHelp(id);
  };
  // A row's link moved the page: keep the topic it was read in.
  const keep = () => openHelp(shown);

  const matches = matchTopics(query, operator);
  const onSearchKey = (event: KeyboardEvent<HTMLInputElement>) => {
    const first = matches[0];
    if (event.key === "Enter" && first) show(first.id);
    if (event.key === "ArrowDown") {
      event.preventDefault();
      panel.current?.querySelector<HTMLElement>("[data-result]")?.focus();
    }
  };

  return (
    <aside
      ref={panel}
      aria-label="Help"
      tabIndex={-1}
      className="help-panel fixed top-(--shell-top) right-0 bottom-0 z-40 flex w-full flex-col border-l border-edge bg-page outline-none min-[900px]:w-[400px]"
    >
      <div className="flex items-center gap-2 border-b border-line px-3.5 py-2">
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onSearchKey}
          placeholder="Search help"
          aria-label="Search help"
          className="min-h-[28px] min-w-0 flex-1 rounded-ctl border border-edge px-2.5 placeholder:text-faint"
        />
        {/* The visible text is the name ("Close esc"), as WCAG 2.5.3 asks. */}
        <button
          type="button"
          className="flex items-center gap-1.5 text-xs text-mut hover:text-fg"
          onClick={closeHelp}
        >
          Close <kbd>esc</kbd>
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3">
        {query ? (
          <Results query={query} matches={matches} onPick={show} />
        ) : browsing ? (
          <TopicList
            operator={operator}
            item={(id) => (
              <button
                type="button"
                aria-current={id === shown ? "true" : undefined}
                className={topicItem}
                onClick={() => show(id)}
              >
                {topics[id].title}
              </button>
            )}
          />
        ) : (
          <div className="flex flex-col gap-5">
            <TopicBody
              id={shown}
              onGo={keep}
              onGuide={closeHelp}
              topicLink={(id, label) => (
                <button type="button" className="lnk" onClick={() => show(id)}>
                  {label}
                </button>
              )}
            />
            <PageKeys />
          </div>
        )}
      </div>
      <div className="flex items-center gap-3.5 border-t border-line px-3.5 py-2 text-xs">
        <button
          type="button"
          className="lnk"
          aria-pressed={browsing}
          onClick={() => {
            setQuery("");
            setBrowsing((b) => !b);
          }}
        >
          {browsing ? "Back to this topic" : "All topics"}
        </button>
        <span className="flex-1" />
        <OpenAsPage topic={shown} />
      </div>
    </aside>
  );
}

/** Search results: each topic with the rows that matched. */
function Results({
  query,
  matches,
  onPick,
}: {
  query: string;
  matches: ReturnType<typeof matchTopics>;
  onPick: (id: TopicId) => void;
}) {
  const here = useCurrentContext();
  if (matches.length === 0)
    return <p className="text-mut">No topic mentions “{query}”.</p>;
  return (
    <ul className="flex flex-col gap-3" aria-label="Results">
      {matches.map((m) => (
        <li key={m.id}>
          <button
            type="button"
            data-result
            className="flex w-full flex-col rounded-ctl px-2 py-1 text-left hover:bg-hover focus-visible:bg-hover"
            onClick={() => onPick(m.id)}
            onKeyDown={(event) => {
              const li = event.currentTarget.closest("li");
              const to =
                event.key === "ArrowDown"
                  ? li?.nextElementSibling
                  : event.key === "ArrowUp"
                    ? li?.previousElementSibling
                    : null;
              if (!to) return;
              event.preventDefault();
              to.querySelector<HTMLElement>("[data-result]")?.focus();
            }}
          >
            <span className="font-medium text-fg">{topics[m.id].title}</span>
            {m.rows.length === 0 ? (
              <span className="text-xs text-mut">{topics[m.id].what}</span>
            ) : (
              m.rows.slice(0, 3).map((row) => (
                <span key={row.what} className="text-xs text-sec">
                  {row.what}
                  {row.cli && (
                    <span className="ml-2 font-mono text-mut">
                      {fillCli(row.cli, here)}
                    </span>
                  )}
                </span>
              ))
            )}
          </button>
        </li>
      ))}
    </ul>
  );
}

/** The topic's own page, for linking and reading at full width; the panel closes. */
function OpenAsPage({ topic }: { topic: TopicId }) {
  const org = useOrg();
  if (!org) return null;
  return (
    <Link
      to="/$org/help/$topic"
      params={{ org, topic }}
      onClick={closeHelp}
      className="lnk"
    >
      Open as page
    </Link>
  );
}

/**
 * This page's keys (the shortcuts mounted pages label), the G sequences
 * that switch tabs, and the keys that work everywhere: what the ? window
 * showed before help took ?.
 */
function PageKeys() {
  const legend = useLegend();
  const page = legend.filter((s) => !s.key.startsWith("g "));
  const go = legend.filter((s) => s.key.startsWith("g "));
  const anywhere = [
    { key: `${modKey.trim()}+K`, label: "search or run anything" },
    { key: "?", label: "open or close help" },
  ];
  return (
    <section aria-label="Keys" className="flex flex-col gap-3">
      {page.length > 0 && <Keys title="Keys on this page" items={page} />}
      {go.length > 0 && <Keys title="Go to" items={go} />}
      <Keys title="Anywhere" items={anywhere} />
    </section>
  );
}

/** A titled list of keys; "g o" reads as G then O. */
function Keys({ title, items }: { title: string; items: LegendItem[] }) {
  return (
    <div className="flex flex-col gap-1">
      <h3 className="text-2xs font-medium text-mut">{title}</h3>
      <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-4 gap-y-1 text-xs">
        {items.map((s) => (
          <div key={`${s.key} ${s.label}`} className="contents">
            <dt className="flex items-center gap-1 text-2xs text-faint">
              {(s.key.startsWith("g ") ? s.key.split(" ") : [s.key]).map(
                (k, i) => (
                  <span key={k} className="contents">
                    {i > 0 && <span>then</span>}
                    <kbd>{k.length === 1 ? k.toUpperCase() : k}</kbd>
                  </span>
                ),
              )}
            </dt>
            <dd className="text-sec">{s.label}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
