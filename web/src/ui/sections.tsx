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
      // 12px under the sticky header: html's scroll-padding-top
      // (index.css) already keeps it clear of the header.
      className="scroll-mt-[12px]"
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
 * The id of the section in view, for the list's mark: the last one whose
 * top passed the line just under the sticky header (where a click in the
 * list puts it: html's scroll padding plus SettingsSection's scroll
 * margin), or the first before any has. At the bottom of the page,
 * where short last sections can't reach that line, the one the #hash
 * names (a click in the list) wins while it's on screen. A passive scroll
 * listener coalesced to one check per frame, so nothing runs while the
 * page is still.
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
    const first = elements[0];
    if (!first) return;
    // 48px of slack marks a section as its heading reaches the header.
    const line =
      Number.parseFloat(
        getComputedStyle(document.documentElement).scrollPaddingTop,
      ) +
      Number.parseFloat(getComputedStyle(first).scrollMarginTop) +
      48;
    let frame = 0;
    const pick = () => {
      frame = 0;
      let id = first.id;
      for (const e of elements) {
        if (e.getBoundingClientRect().top <= line) id = e.id;
      }
      const root = document.documentElement;
      const hashed = document.getElementById(location.hash.slice(1));
      if (
        scrollY + innerHeight >= root.scrollHeight - 2 &&
        hashed &&
        elements.includes(hashed) &&
        hashed.getBoundingClientRect().top < innerHeight
      ) {
        id = hashed.id;
      }
      setCurrent(id);
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(pick);
    };
    // A #hash in the address scrolls to its section once it's rendered.
    const hash = location.hash.slice(1);
    if (hash) document.getElementById(hash)?.scrollIntoView();
    pick();
    addEventListener("scroll", schedule, { passive: true });
    addEventListener("hashchange", schedule);
    addEventListener("resize", schedule);
    return () => {
      cancelAnimationFrame(frame);
      removeEventListener("scroll", schedule);
      removeEventListener("hashchange", schedule);
      removeEventListener("resize", schedule);
    };
  }, [ids]);
  return current;
}

/**
 * A settings page short enough to need no list of sections (a project's
 * Settings and Project settings, ADR 0099): one row per setting, its name
 * on the left and what it is, with its buttons, beside it. `id` is the
 * address's #hash.
 */
export function SettingRow({
  id,
  label,
  danger,
  children,
}: {
  id: string;
  label: ReactNode;
  /** The row that archives or deletes: its name in red. */
  danger?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      className="grid scroll-mt-[12px] gap-x-6 gap-y-2 border-b border-line py-3 first:border-t md:grid-cols-[170px_minmax(0,1fr)]"
    >
      <h2
        id={`${id}-h`}
        className={`font-semibold ${danger ? "text-fail" : ""}`}
      >
        {label}
      </h2>
      <div className="min-w-0">{children}</div>
    </section>
  );
}
