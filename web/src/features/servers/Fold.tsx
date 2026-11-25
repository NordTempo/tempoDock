import type { ReactNode } from "react";

/**
 * One section of a server's page as a row (ADR 0099): its name, its
 * state in a few words, an action where the section has one (Prune,
 * Drain), and a chevron; a click opens the section in place under it.
 * The page reads as a summary first, the details one click away.
 */
export function Fold({
  id,
  label,
  summary,
  action,
  open,
  onToggle,
  children,
}: {
  id: string;
  label: string;
  summary: ReactNode;
  action?: ReactNode;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      className="scroll-mt-[calc(var(--shell-top)+80px)] border-t border-line last:border-b"
    >
      <div className="flex min-h-[44px] items-center gap-4">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={`${id}-body`}
          onClick={onToggle}
          className="flex min-h-[44px] min-w-0 flex-1 items-center gap-4 text-left hover:bg-hover"
        >
          <h2 id={`${id}-h`} className="w-36 shrink-0 font-semibold">
            {label}
          </h2>
          <span className="min-w-0 flex-1 truncate text-mut">{summary}</span>
          <span
            aria-hidden
            className={`pr-1 text-faint ${open ? "rotate-90" : ""}`}
          >
            ›
          </span>
        </button>
        {action && <span className="shrink-0">{action}</span>}
      </div>
      {open && (
        <div id={`${id}-body`} className="flex flex-col gap-3 pt-1 pb-5">
          {children}
        </div>
      )}
    </section>
  );
}
