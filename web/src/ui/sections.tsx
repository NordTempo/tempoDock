import { type ReactNode, useEffect, useState } from "react";

/**
 * One section of a settings page: its anchor id (also the address's
 * #hash), its name in the list, and `danger` for the last one that
 * archives or deletes (red, set apart).
 */
export type SectionItem = { id: string; label: string; danger?: boolean };

/**
 * Every Settings page in Dock has this one shape: a list of its sections
 * on the left, all of them on one page beside it. A click in the list
 * scrolls to the section; scrolling marks the section in view. Below
 * `md` the list becomes a row of links over the page. Used by the
 * service, project and organization settings.
 */
export function SectionsLayout({
  label,
  sections,
  children,
}: {
  /** Names the list for screen readers: "web settings". */
  label: string;
  sections: SectionItem[];
  children: ReactNode;
}) {
  const current = useSectionInView(sections);
  return (
    <div className="flex min-w-0 flex-1 flex-col md:flex-row">
      <nav
        aria-label={label}
        className="flex gap-1 overflow-x-auto border-b border-line px-3 py-2 md:sticky md:top-[calc(var(--shell-top)+var(--sections-top,112px))] md:flex md:max-h-[calc(100vh-var(--shell-top)-var(--sections-top,112px))] md:w-[210px] md:shrink-0 md:flex-col md:self-start md:overflow-y-auto md:border-b-0 md:py-5"
      >
        {sections.map((s) => (
          <a
            key={s.id}
            href={`#${s.id}`}
            aria-current={s.id === current ? "location" : undefined}
            className={`rounded-ctl px-2.5 py-1.5 whitespace-nowrap no-underline hover:bg-hover aria-[current]:bg-sel aria-[current]:text-fg ${s.danger ? "text-fail md:mt-3" : "text-sec"}`}
          >
            {s.label}
          </a>
        ))}
      </nav>
      <div className="flex min-w-0 flex-1 flex-col">{children}</div>
    </div>
  );
}

/**
 * A section on a settings page: its heading (with actions on the right)
 * and its rows. `id` matches a SectionItem's, so the list and the
 * address's #hash find it.
 */
export function SettingsSection({
  id,
  title,
  actions,
  children,
}: {
  id: string;
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      className="scroll-mt-[calc(var(--shell-top)+var(--sections-top,112px)+12px)]"
    >
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id={`${id}-h`} className="text-lg font-semibold">
          {title}
        </h2>
        {actions && (
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {actions}
          </div>
        )}
      </div>
      {children}
    </section>
  );
}

/**
 * The id of the section in view, for the list's mark: the one whose top
 * last passed the upper third of the window. Starts from the address's
 * #hash. An IntersectionObserver, so nothing runs while the page is still.
 */
function useSectionInView(sections: SectionItem[]) {
  const ids = sections.map((s) => s.id).join(" ");
  const [current, setCurrent] = useState<string | undefined>(
    () => location.hash.slice(1) || sections[0]?.id,
  );
  useEffect(() => {
    const elements = ids
      .split(" ")
      .map((id) => document.getElementById(id))
      .filter((e): e is HTMLElement => e !== null);
    if (elements.length === 0) return;
    const visible = new Set<string>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) visible.add(e.target.id);
          else visible.delete(e.target.id);
        }
        // The lowest one in the top third: its heading passed the line
        // last, so it's the one being read (and a #hash jump's target,
        // not the end of the section above it).
        const last = elements.findLast((e) => visible.has(e.id));
        if (last) setCurrent(last.id);
      },
      { rootMargin: "0px 0px -66% 0px" },
    );
    for (const e of elements) observer.observe(e);
    // A #hash in the address scrolls to its section once it's rendered.
    const hash = location.hash.slice(1);
    if (hash) document.getElementById(hash)?.scrollIntoView();
    return () => observer.disconnect();
  }, [ids]);
  return current;
}
