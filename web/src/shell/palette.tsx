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
import { ErrorText, Kbd } from "../ui/kit";
import { type Match, matchCommands, useCommands } from "./commands";
import { useKeydown } from "./keys";
import { useCurrentContext } from "./location";

const PaletteContext = createContext<() => void>(() => {});

/** Opens the command palette. */
export const usePalette = () => use(PaletteContext);

/** Owns the palette's open state and the global Cmd+K / Ctrl+K binding. */
export function PaletteProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  useKeydown((event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      setOpen((o) => !o);
    }
  });
  return (
    <PaletteContext value={() => setOpen(true)}>
      {children}
      {open && <Palette onClose={() => setOpen(false)} />}
    </PaletteContext>
  );
}

function Palette({ onClose }: { onClose: () => void }) {
  const commands = useCommands();
  const here = useCurrentContext();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [error, setError] = useState<Error | null>(null);
  const [busy, setBusy] = useState(false);
  const list = useRef<HTMLDivElement>(null);
  const matches = useMemo(
    () => matchCommands(commands, query),
    [commands, query],
  );
  const selected = matches[Math.min(index, matches.length - 1)];

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

  async function run(match: Match | undefined) {
    if (!match || busy) return;
    setBusy(true);
    setError(null);
    try {
      await match.command.run();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
      setBusy(false);
    }
  }

  function onKeyDown(event: KeyboardEvent) {
    const move = (delta: number) => {
      event.preventDefault();
      setIndex(
        (i) => (i + delta + matches.length) % Math.max(1, matches.length),
      );
    };
    if (event.key === "ArrowDown") move(1);
    else if (event.key === "ArrowUp") move(-1);
    else if (event.key === "Enter") {
      event.preventDefault();
      run(selected);
    } else if (event.key === "Escape") {
      event.preventDefault();
      onClose();
    }
  }

  const contextLabel = [here.project, here.environment]
    .filter(Boolean)
    .join(" / ");

  return (
    <>
      <div
        className="fixed inset-0 z-20 bg-black/70"
        onClick={onClose}
        aria-hidden
      />
      <div
        role="dialog"
        aria-modal
        aria-label="Command palette"
        className="fixed top-[12vh] left-1/2 z-30 w-[640px] max-w-[calc(100vw-32px)] -translate-x-1/2 border border-[#3a3a3a] bg-black"
        onKeyDown={onKeyDown}
      >
        <div className="flex items-center gap-2.5 border-b border-line2 px-3.5 py-3 text-lg">
          <span className="text-dim">›</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the palette exists to be typed into
            autoFocus
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setIndex(0);
            }}
            placeholder="Type a command or a name"
            aria-label="Command"
            role="combobox"
            aria-expanded
            aria-controls="palette-list"
            className="min-w-0 flex-1 bg-black outline-none placeholder:text-faint"
          />
          <span className="text-xs text-dim">{contextLabel}</span>
        </div>

        <div
          ref={list}
          id="palette-list"
          role="listbox"
          className="max-h-[52vh] overflow-auto pb-1"
        >
          {matches.length === 0 && (
            <div className="px-3.5 py-3 text-dim">No matches</div>
          )}
          {matches.map((match, i) => {
            const groupStart =
              matches[i - 1]?.command.group !== match.command.group;
            const isSelected = match === selected;
            return (
              <div key={match.command.id}>
                {groupStart && (
                  <div className="px-3.5 pt-2 pb-0.5 text-2xs tracking-[.06em] text-dim uppercase">
                    {match.command.group}
                  </div>
                )}
                {/* biome-ignore lint/a11y/useKeyWithClickEvents: keys go through the combobox input */}
                <div
                  role="option"
                  aria-selected={isSelected}
                  tabIndex={-1}
                  onMouseMove={() => setIndex(i)}
                  onClick={() => run(match)}
                  className={`grid cursor-pointer grid-cols-[1fr_auto] gap-3 px-3.5 py-[5px] ${
                    isSelected ? "bg-sel shadow-[inset_2px_0_0_#fff]" : ""
                  }`}
                >
                  <span className="truncate">
                    <Highlight
                      text={match.command.label}
                      ranges={match.ranges}
                    />
                  </span>
                  <span className="text-dim">
                    {isSelected ? <Kbd>↵</Kbd> : match.command.hint}
                  </span>
                </div>
              </div>
            );
          })}
        </div>

        <div className="flex gap-4 border-t border-line2 px-3.5 py-2 text-xs text-dim">
          <span>
            <Kbd>↑↓</Kbd> move
          </span>
          <span>
            <Kbd>↵</Kbd> run
          </span>
          <span>
            <Kbd>esc</Kbd> close
          </span>
          <span className="flex-1" />
          {error ? (
            <ErrorText error={error} />
          ) : (
            selected && (
              <span className="truncate font-mono">
                equivalent: {selected.command.cli}
              </span>
            )
          )}
        </div>
      </div>
    </>
  );
}

function Highlight({
  text,
  ranges,
}: {
  text: string;
  ranges: Array<[number, number]>;
}) {
  if (ranges.length === 0) return text;
  const marked = new Array<boolean>(text.length).fill(false);
  for (const [from, to] of ranges) marked.fill(true, from, to);
  const parts: Array<{ text: string; marked: boolean }> = [];
  for (let i = 0; i < text.length; i++) {
    const last = parts.at(-1);
    if (last && last.marked === marked[i]) last.text += text[i];
    else parts.push({ text: text[i] ?? "", marked: marked[i] ?? false });
  }
  return parts.map((part, i) =>
    part.marked ? (
      <em key={i} className="not-italic underline underline-offset-3">
        {part.text}
      </em>
    ) : (
      part.text
    ),
  );
}
