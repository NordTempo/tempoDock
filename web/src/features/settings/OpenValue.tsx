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
