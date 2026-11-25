import type { ReactNode } from "react";

/**
 * A settings value that opens its editor: the whole value is the target,
 * with a pointer, a hover and a chevron at the line's end, so "click to
 * change" reads without an Edit button on every row. `label` names it for
 * screen readers ("Edit port"). Without `onOpen` it is the value alone,
 * for people who can't change it.
 */
export function OpenValue({
  label,
  onOpen,
  children,
}: {
  label: string;
  onOpen?: () => void;
  children: ReactNode;
}) {
  if (!onOpen) return <span className="block min-w-0">{children}</span>;
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onOpen}
      className="group -mx-2 -my-1 flex w-[calc(100%+16px)] max-w-[776px] cursor-pointer items-start gap-3 rounded-ctl px-2 py-1 text-left hover:bg-hover"
    >
      <span className="min-w-0 flex-1">{children}</span>
      <span aria-hidden className="text-faint group-hover:text-fg">
        ›
      </span>
    </button>
  );
}

/**
 * Settings left on their defaults, folded into one row: "2 on defaults",
 * what they are, and Show. Shown, the rows themselves follow and the row
 * says Hide. Rows that are set always show; this keeps them in front.
 */
export function Folded({
  summary,
  open,
  onToggle,
}: {
  /** What the folded settings are: "start command (the image's), limits". */
  summary: ReactNode[];
  open: boolean;
  onToggle: () => void;
}) {
  if (summary.length === 0) return null;
  return (
    <>
      <dt>{summary.length} on defaults</dt>
      <dd>
        <button
          type="button"
          aria-expanded={open}
          onClick={onToggle}
          className="-mx-2 -my-1 flex w-[calc(100%+16px)] max-w-[776px] cursor-pointer items-start gap-3 rounded-ctl px-2 py-1 text-left text-mut hover:bg-hover"
        >
          <span className="min-w-0 flex-1">
            {summary.map((s, i) => (
              // Positional: rebuilt from the same rows each render.
              <span key={i}>
                {i > 0 && ", "}
                {s}
              </span>
            ))}
          </span>
          <span className="lnk text-fg">{open ? "Hide" : "Show"}</span>
        </button>
      </dd>
    </>
  );
}
