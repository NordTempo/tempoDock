import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { reconnectLive, useLiveStatus } from "../data/liveStatus";
import { ErrorText } from "../ui/kit";
import {
  type Command,
  matchCommands,
  rememberCommand,
  useCommands,
} from "./commands";
import { isTyping, modKey, useKeydown, useLegend } from "./keys";

/**
 * The bottom command line: type a `dock` command (or any part of a palette
 * label), Tab completes the best match, Enter runs it. `?` shows the keys.
 * When idle it lists the current page's shortcuts.
 */
export function CommandLine() {
  const commands = useCommands();
  const legend = useLegend();
  const input = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [focused, setFocused] = useState(false);
  const [help, setHelp] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);

  const matches = useMemo(
    () =>
      text.trim()
        ? matchCommands(commands, text)
            .filter((m) => m.command.cli.startsWith("dock"))
            .slice(0, 6)
        : [],
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

  const legendText = legend.map((s) => `${s.key.toUpperCase()} ${s.label}`);

  return (
    <div className="sticky bottom-0 z-20 bg-page px-5 pt-1.5 pb-3">
      <div className="mx-auto max-w-[860px] overflow-hidden rounded-xl border border-edge bg-panel">
        <NotLive />
        {help && <Help onClose={() => setHelp(false)} />}
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
                <span className="font-mono text-xs">{command.cli}</span>
                <span className="text-mut">{command.label}</span>
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
          <label htmlFor="cmd" className="font-mono text-faint">
            ›
          </label>
          <input
            ref={input}
            id="cmd"
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
                setText(best.cli);
              } else if (e.key === "Escape") {
                setText("");
                e.currentTarget.blur();
              }
            }}
            placeholder="Run a dock command, or ? for keys"
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

function Help({ onClose }: { onClose: () => void }) {
  const legend = useLegend();
  const global = [
    [`${modKey.trim()}K`, "search or run anything"],
    ["?", "show or hide these keys"],
    ["esc", "close"],
  ];
  return (
    <section
      aria-label="Keyboard shortcuts"
      className="border-b border-line px-3.5 py-3"
    >
      <div className="flex items-baseline gap-3 pb-2">
        <h2 className="text-sm font-semibold">Keys</h2>
        <button type="button" className="lnk ml-auto" onClick={onClose}>
          Close
        </button>
      </div>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
        {[...legend.map((s) => [s.key.toUpperCase(), s.label]), ...global].map(
          ([key, label]) => (
            <div key={`${key} ${label}`} className="contents">
              <dt>
                <kbd>{key}</kbd>
              </dt>
              <dd className="text-mut">{label}</dd>
            </div>
          ),
        )}
      </dl>
    </section>
  );
}
