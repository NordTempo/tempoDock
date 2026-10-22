import {
  createContext,
  type KeyboardEvent,
  type ReactNode,
  use,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Icon } from "../ui/icons";
import { ErrorText } from "../ui/kit";
import { toneText } from "../ui/status";
import {
  type Command,
  type Match,
  matchCommands,
  recentIds,
  rememberCommand,
  useCommands,
} from "./commands";
import { modKey, useKeydown } from "./keys";
import { useCurrentContext } from "./location";

/** Opens the command palette, optionally with a query typed in. */
const PaletteContext = createContext<(query?: string) => void>(() => {});

export const usePalette = () => use(PaletteContext);

/** Owns the palette's open state and the global Cmd+K / Ctrl+K binding. */
export function PaletteProvider({ children }: { children: ReactNode }) {
  const [query, setQuery] = useState<string | null>(null);
  useKeydown((event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      setQuery((q) => (q === null ? "" : null));
    }
  });
  return (
    <PaletteContext value={(q = "") => setQuery(q)}>
      {children}
      {query !== null && (
        <Palette initial={query} onClose={() => setQuery(null)} />
      )}
    </PaletteContext>
  );
}

type Row = Match & { group: string };

function Palette({
  initial,
  onClose,
}: {
  initial: string;
  onClose: () => void;
}) {
  const commands = useCommands();
  const here = useCurrentContext();
  const [query, setQuery] = useState(initial);
  const [index, setIndex] = useState(0);
  const [error, setError] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);
  const list = useRef<HTMLDivElement>(null);

  const rows = useMemo((): Row[] => {
    const matches = matchCommands(commands, query).map((m) => ({
      ...m,
      group: m.command.group,
    }));
    if (query) return matches;
    // With nothing typed, the last few commands come first.
    const recent = recentIds().flatMap((id) => {
      const command = commands.find((c) => c.id === id);
      return command ? [{ command, score: 0, group: "Recent" }] : [];
    });
    const seen = new Set(recent.map((r) => r.command.id));
    return [...recent, ...matches.filter((m) => !seen.has(m.command.id))];
  }, [commands, query]);
  const selected = rows[Math.min(index, rows.length - 1)];

  // Hand focus back to wherever it was when the palette opened.
  useEffect(() => {
    const previous = document.activeElement;
    return () => {
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);

  useEffect(() => {
    list.current
      ?.querySelector("[aria-selected=true]")
      ?.scrollIntoView({ block: "nearest" });
  });

  async function run(command: Command | undefined) {
    if (!command || busy) return;
    setBusy(true);
    setError(null);
    try {
      rememberCommand(command.id);
      await command.run();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
      setBusy(false);
    }
  }

  function onKeyDown(event: KeyboardEvent) {
    const move = (delta: number) => {
      event.preventDefault();
      setIndex((i) => (i + delta + rows.length) % Math.max(1, rows.length));
    };
    if (event.key === "ArrowDown") move(1);
    else if (event.key === "ArrowUp") move(-1);
    else if (event.key === "Enter") {
      event.preventDefault();
      run(selected?.command);
    } else if (event.key === "Escape") {
      event.preventDefault();
      onClose();
    } else if (
      (event.metaKey || event.ctrlKey) &&
      event.key === "c" &&
      selected?.command.cli.startsWith("dock") &&
      !window.getSelection()?.toString()
    ) {
      event.preventDefault();
      navigator.clipboard.writeText(selected.command.cli);
    }
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a click outside the dialog closes it; Escape does too
    <div
      className="fixed inset-0 z-30 flex items-start justify-center bg-black/60 px-4 pt-24"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal
        aria-label="Command palette"
        className="w-full max-w-[720px] overflow-hidden rounded-panel border border-edge bg-panel shadow-2xl shadow-black/50"
        onKeyDown={onKeyDown}
      >
        <div className="flex items-center gap-2.5 border-b border-line px-4 py-3.5">
          <Icon name="search" className="text-mut" />
          <input
            // biome-ignore lint/a11y/noAutofocus: the palette exists to be typed into
            autoFocus
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setIndex(0);
            }}
            placeholder="Search or run"
            aria-label="Search or run"
            role="combobox"
            aria-expanded
            aria-controls="palette-list"
            className="min-w-0 flex-1 bg-transparent text-md outline-none"
          />
          {here.project && <span className="pill">{here.project}</span>}
        </div>

        <div
          ref={list}
          id="palette-list"
          role="listbox"
          aria-label="Results"
          className="max-h-[56vh] overflow-auto pb-1"
        >
          {rows.length === 0 && (
            <div className="px-4 py-3 text-mut">No matches</div>
          )}
          {rows.map((row, i) => (
            <div key={`${row.group}:${row.command.id}`}>
              {rows[i - 1]?.group !== row.group && (
                <div className="px-4 pt-2.5 pb-1 text-2xs font-medium text-faint">
                  {row.group}
                </div>
              )}
              <Item
                command={row.command}
                selected={row === selected}
                onHover={() => setIndex(i)}
                onRun={() => run(row.command)}
              />
            </div>
          ))}
        </div>

        <div className="flex flex-wrap gap-x-[18px] gap-y-1.5 border-t border-line px-4 py-2.5 text-xs text-faint">
          <span>
            <kbd>↑</kbd> <kbd>↓</kbd> move
          </span>
          <span>
            <kbd>↵</kbd> run
          </span>
          <span>
            <kbd>{modKey.trim()}C</kbd> copy command
          </span>
          <ErrorText error={error} />
          <span className="ml-auto">
            <kbd>esc</kbd> close
          </span>
        </div>
      </div>
    </div>
  );
}

function Item({
  command,
  selected,
  onHover,
  onRun,
}: {
  command: Command;
  selected: boolean;
  onHover: () => void;
  onRun: () => void;
}) {
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: keys go through the combobox input
    <div
      role="option"
      aria-selected={selected}
      tabIndex={-1}
      onMouseMove={onHover}
      onClick={onRun}
      className={`mx-1 flex cursor-pointer flex-wrap items-baseline gap-x-3.5 gap-y-0.5 rounded-ctl px-3 py-[7px] ${
        selected ? "bg-sel" : "hover:bg-hover"
      }`}
    >
      <span>
        <Label text={command.label} strong={command.strong} />
      </span>
      {command.note && (
        <span className={`text-xs ${toneText[command.note.tone]}`}>
          {command.note.text}
        </span>
      )}
      <span
        className={`ml-auto font-mono text-2xs ${selected ? "text-sec" : "text-faint"}`}
      >
        {command.cli}
      </span>
    </div>
  );
}

/** The label with its strong part bold and release numbers in mono. */
function Label({ text, strong }: { text: string; strong?: string }) {
  return text.split(/(\s+)/).map((word, i) =>
    word === strong ? (
      <b key={i} className="font-semibold">
        {word}
      </b>
    ) : /^r\d+$/.test(word) ? (
      <span key={i} className="font-mono">
        {word}
      </span>
    ) : (
      word
    ),
  );
}
