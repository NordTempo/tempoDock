import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { since } from "../../ui/format";
import { input } from "../create/form";

type Repo = components["schemas"]["GitHubRepo"];

/**
 * A filterable list to pick one item from, keyboard first: typing
 * narrows it, ↑↓ move, ↵ picks. At most `max` rows show; with `typed`,
 * ↵ on text that matches nothing hands the text over (a directory that
 * isn't listed). Used by the "From GitHub" flow and Settings › Source.
 */
export function PickList<T>({
  items,
  keyOf,
  textOf,
  row,
  onPick,
  typed,
  label,
  placeholder,
  initial = "",
  max = 50,
  focus = true,
  height = "max-h-72",
  onEscape,
  loading,
}: {
  items: readonly T[];
  keyOf: (item: T) => string;
  /** What typing matches against. */
  textOf: (item: T) => string;
  /** One row's contents; `on` is the row ↵ would pick. */
  row: (item: T, on: boolean) => ReactNode;
  onPick: (item: T) => void;
  typed?: (text: string) => void;
  label: string;
  placeholder: string;
  initial?: string;
  max?: number;
  focus?: boolean;
  height?: string;
  /** Esc in the filter, to close what opened the list. */
  onEscape?: () => void;
  /** The items are still being read (from GitHub). */
  loading?: boolean;
}) {
  const [filter, setFilter] = useState(initial);
  const [at, setAt] = useState(0);
  const q = filter.trim().toLowerCase();
  const matches = q
    ? items.filter((i) => textOf(i).toLowerCase().includes(q))
    : items;
  const shown = matches.slice(0, max);
  const current = shown[Math.min(at, shown.length - 1)];
  const id = `pick-${label.toLowerCase().replace(/\W+/g, "-")}`;
  return (
    <div className="flex min-w-0 flex-col">
      <input
        ref={(el) => {
          if (focus && el && !el.dataset.focused) {
            el.dataset.focused = "1";
            el.focus();
          }
        }}
        aria-label={label}
        aria-controls={id}
        value={filter}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        onChange={(e) => {
          setFilter(e.target.value);
          setAt(0);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            const step = e.key === "ArrowDown" ? 1 : -1;
            setAt((i) =>
              Math.max(
                0,
                Math.min(
                  shown.length - 1,
                  Math.min(i, shown.length - 1) + step,
                ),
              ),
            );
          } else if (e.key === "Escape" && onEscape) {
            e.preventDefault();
            onEscape();
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (current) onPick(current);
            else if (typed && filter.trim()) typed(filter.trim());
          }
        }}
        className={`${input} w-full`}
      />
      <div
        id={id}
        role="listbox"
        aria-label={label}
        className={`mt-1.5 ${height} overflow-y-auto border-line border-t`}
      >
        {shown.map((item) => (
          <button
            key={keyOf(item)}
            type="button"
            role="option"
            aria-selected={item === current}
            onClick={() => onPick(item)}
            className={`flex w-full cursor-pointer items-center gap-3 border-soft border-b px-2 py-1.5 text-left hover:bg-hover ${item === current ? "bg-sel" : ""}`}
          >
            {row(item, item === current)}
          </button>
        ))}
        {shown.length === 0 && loading && (
          <p className="px-2 py-2 text-mut text-xs">Reading from GitHub…</p>
        )}
        {shown.length === 0 && !loading && (
          <p className="px-2 py-2 text-mut text-xs">
            Nothing matches {filter.trim()}
            {typed && filter.trim() ? "; ↵ uses it as typed." : "."}
          </p>
        )}
      </div>
      {matches.length > shown.length && (
        <span className="mt-1 text-faint text-xs">
          {shown.length} of {matches.length} shown; type to narrow.
        </span>
      )}
    </div>
  );
}

/**
 * The repositories the org's GitHub App reads, as a PickList: the newest
 * push first, with the services already deploying from each.
 */
export function RepoPicker({
  repos,
  onPick,
  onEscape,
  loading,
}: {
  repos: readonly Repo[];
  onPick: (repo: Repo) => void;
  onEscape?: () => void;
  loading?: boolean;
}) {
  return (
    <PickList
      items={repos}
      keyOf={(r) => String(r.id)}
      textOf={(r) => r.full_name}
      label="Repositories"
      placeholder={`Filter ${repos.length} ${repos.length === 1 ? "repository" : "repositories"}, ↵ picks`}
      onPick={onPick}
      onEscape={onEscape}
      loading={loading}
      row={(r, on) => (
        <>
          <span
            className={`min-w-0 flex-1 truncate font-mono ${on ? "text-fg" : "text-sec"}`}
          >
            {r.full_name}
          </span>
          <span className="truncate text-mut text-xs">
            {(r.used_by ?? [])
              .map((u) => u.split(":")[0]?.split("/").at(-1))
              .join(", ")}
          </span>
          <span className="text-faint text-xs">
            {r.private ? "private" : ""}
          </span>
          <span className="w-8 text-right text-faint text-xs">
            {r.pushed_at ? since(r.pushed_at) : ""}
          </span>
        </>
      )}
    />
  );
}
