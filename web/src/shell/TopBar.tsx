import type { ReactNode } from "react";

/** Sticky page header: breadcrumb and status on the left, actions right. */
export function TopBar({
  children,
  actions,
}: {
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="sticky top-0 z-10 flex h-[41px] items-center gap-3.5 border-b border-line bg-black px-5">
      {children}
      <span className="flex-1" />
      {actions}
    </header>
  );
}

/** "shop / production / api": every part but the last is dim. */
export function Crumbs({ children }: { children: ReactNode[] }) {
  return (
    <span className="flex items-baseline gap-1.5 whitespace-nowrap">
      {children.map((part, i) => {
        const last = i === children.length - 1;
        return (
          <span key={i} className="flex gap-1.5">
            <span className={last ? "font-semibold" : "text-dim"}>{part}</span>
            {!last && <span className="text-dim">/</span>}
          </span>
        );
      })}
    </span>
  );
}
