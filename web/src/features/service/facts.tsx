import type { ReactNode } from "react";

// The pieces of a service Overview's fact rows, shared by ServiceFacts
// and the uptime and visitors rows (Signals.tsx).

/** A fact row: label, value, chevron; the whole row opens its home. */
export const factRow =
  "grid min-h-[40px] grid-cols-[130px_minmax(0,1fr)_16px] items-center gap-x-4 border-t border-soft py-2 text-xs text-fg no-underline hover:bg-hover";

/** A fact row's label. */
export function FactLabel({ children }: { children: ReactNode }) {
  return <span className="text-mut">{children}</span>;
}

/** The chevron in a fact row's last cell: the row opens something. */
export function Chevron() {
  return (
    <span aria-hidden className="text-right text-faint">
      ›
    </span>
  );
}
