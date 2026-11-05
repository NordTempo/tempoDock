import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { reconnectLive, useLiveStatus } from "../data/liveStatus";
import { Dialog } from "../ui/Dialog";
import { ErrorText } from "../ui/kit";
import {
  type Command,
  matchCommands,
  rememberCommand,
  useCommands,
} from "./commands";
import {
  isTyping,
  type LegendItem,
  modKey,
  useKeydown,
  useLegend,
} from "./keys";

/**
 * The bottom command line: type a `dock` command or any part of a palette
 * label ("logs api", "members"), Tab completes the best match, Enter runs
 * it. `?` shows the keys.
 * When idle it lists the current page's shortcuts. It sticks to the
 * bottom of the window, set off by a line, and sits after the page's
 * last row once scrolled to the end.
 */
export function CommandLine() {
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [focused, setFocused] = useState(false);
  // Release history (for rollback commands) loads once it's in use.
  const commands = useCommands({ withHistory: focused });
  const legend = useLegend();
  const [help, setHelp] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);

  const matches = useMemo(
    () => (text.trim() ? matchCommands(commands, text).slice(0, 6) : []),
    [commands, text],
  );
  const best = matches[0]?.command;

  useKeydown((event) => {
    if (event.key === "?" && !isTyping(event)) {
      event.preventDefault();
      setHelp((h) => !h);
    } else if (event.key === "Escape" && help) {
      setHelp(false);
    }
  });

  async function run(command: Command | undefined) {
    if (!command || busy) return;
    setBusy(true);
    setError(null);
    try {
      rememberCommand(command.id);
      await command.run();
      setText("");
      input.current?.blur();
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setBusy(false);
    }
  }

  // G-sequences (tabs) are many; ? lists them, the idle line doesn't.
  const legendText = legend
    .filter((s) => !s.key.startsWith("g "))
    .map((s) => `${s.key.toUpperCase()} ${s.label}`);

  return (
    <div className="sticky bottom-0 z-20 border-t border-line bg-page px-5 pt-2 pb-3">
      {help && <Help onClose={() => setHelp(false)} />}
      <div className="mx-auto max-w-[860px] overflow-hidden rounded-xl border border-edge bg-panel">
        <NotLive />
        {focused && matches.length > 0 && (
          <ul
            aria-label="Matching commands"
            className="border-b border-line py-1"
          >
            {matches.map(({ command }) => (
              <li
                key={command.id}
                className={`flex gap-4 px-3.5 py-1 ${command === best ? "bg-sel" : ""}`}
              >
                {isCli(command) ? (
                  <>
                    <span className="font-mono text-xs">{command.cli}</span>
                    <span className="text-mut">{command.label}</span>
                  </>
                ) : (
                  <>
                    <span>{command.label}</span>
                    <span className="text-faint">{command.group}</span>
                  </>
                )}
              </li>
            ))}
          </ul>
        )}
        <form
          aria-label="Command line"
          className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-3.5 py-2"
          onSubmit={(e) => {
            e.preventDefault();
            run(best);
          }}
        >
          <span aria-hidden className="font-mono text-faint">
            ›
          </span>
          <input
            ref={input}
            id="cmd"
            aria-label="Command: a dock command or a page to go to"
            autoComplete="off"
            spellCheck={false}
            value={text}
            onChange={(e) => {
              setError(null);
              if (e.target.value === "?") {
                setHelp(true);
                return;
              }
              setText(e.target.value);
            }}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            onKeyDown={(e) => {
              if (e.key === "Tab" && best && text) {
                e.preventDefault();
                setText(isCli(best) ? best.cli : best.label);
              } else if (e.key === "Escape") {
                setText("");
                e.currentTarget.blur();
              }
            }}
            placeholder="Run a dock command or go to a page, ? for keys"
            className="min-w-0 flex-[1_1_240px] py-1 font-mono text-sm outline-none"
          />
          {error ? (
            <ErrorText error={error} />
          ) : (
            <span className="text-xs text-faint">
              {focused || text
                ? "↵ run · tab complete"
                : legendText.join(" · ") || "↵ run · tab complete"}
            </span>
          )}
        </form>
      </div>
    </div>
  );
}

/**
 * Says so when the live stream is down, so a page that stopped updating
 * doesn't pass for current: retrying, or closed with a Reconnect button.
 */
function NotLive() {
  const status = useLiveStatus();
  const queryClient = useQueryClient();
  if (status === "live") return null;
  return (
    <p
      role="status"
      className="flex flex-wrap items-center gap-x-2.5 border-b border-line px-3.5 py-1.5 text-xs text-warn"
    >
      <span>
        <span aria-hidden>○</span> Not live:{" "}
        {status === "dropped"
          ? "lost the connection to Dock, retrying. Pages may be out of date."
          : "updates stopped. Pages may be out of date."}
      </span>
      {status === "closed" && (
        <button
          type="button"
          className="lnk text-warn underline"
          onClick={() => {
            reconnectLive();
            void queryClient.invalidateQueries();
          }}
        >
          Reconnect
        </button>
      )}
    </p>
  );
}

/** Commands the CLI has too complete to their `dock` line, others to their label. */
const isCli = (command: Command) => command.cli.startsWith("dock");

/**
 * The ? window: this page's keys, the G sequences that switch tabs, and
 * the keys that work everywhere, in a modal like the palette.
 */
function Help({ onClose }: { onClose: () => void }) {
  const legend = useLegend();
  const page = legend.filter((s) => !s.key.startsWith("g "));
  const go = legend.filter((s) => s.key.startsWith("g "));
  const anywhere = [
    { key: `${modKey.trim()}+K`, label: "search or run anything" },
    { key: "?", label: "show these keys" },
    { key: "esc", label: "close" },
  ];
  return (
    <Dialog
      title="Keyboard shortcuts"
      bare
      onClose={onClose}
      className="m-auto max-w-[min(640px,calc(100vw-2rem))]"
    >
      <div className="flex items-center border-b border-line px-5 py-3">
        <h2 className="text-md font-semibold">Keyboard shortcuts</h2>
        <button
          type="button"
          className="lnk ml-auto"
          aria-label="Close"
          onClick={onClose}
        >
          <kbd>esc</kbd>
        </button>
      </div>
      {/* Focus lands here, not on the close button. */}
      <div
        data-autofocus
        tabIndex={-1}
        className="grid gap-x-8 gap-y-5 px-5 py-4 outline-none sm:grid-cols-2"
      >
        <div className="flex flex-col gap-5">
          {page.length > 0 && <Keys title="This page" items={page} />}
          <Keys title="Anywhere" items={anywhere} />
        </div>
        {go.length > 0 && <Keys title="Go to" items={go} />}
      </div>
    </Dialog>
  );
}

/** A titled list of keys; "g o" reads as G then O. */
function Keys({ title, items }: { title: string; items: LegendItem[] }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-2xs font-medium text-mut">{title}</h3>
      <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-4 gap-y-1.5">
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
    </section>
  );
}
