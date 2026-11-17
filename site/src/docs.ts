import { type CollectionEntry, getCollection } from "astro:content";
import { guideHref, sections } from "./guide";

/** One guide page: its entry, title (the page's first heading) and address. */
export interface Guide {
  slug: string;
  title: string;
  href: string;
  /** What the guide is for, from the README's table. */
  summary: string;
  entry: CollectionEntry<"guide">;
}

/** A page's title: its first heading. */
const titleOf = (entry: CollectionEntry<"guide">) =>
  /^# (.+)$/m.exec(entry.body ?? "")?.[1] ?? entry.id;

/** Markdown inline marks and links off a README table cell. */
const plain = (cell: string) =>
  cell.replace(/\[([^\]]+)\]\([^)]+\)/g, "$1").replace(/[`*]/g, "");

/**
 * The docs: the front page (README.md) and the guides in their sections,
 * in reading order. Fails when docs/guide and the sections (guide.ts)
 * disagree about which pages exist.
 */
export async function loadDocs() {
  const entries = await getCollection("guide");
  const byId = new Map(entries.map((entry) => [entry.id, entry]));
  const front = byId.get("readme");
  if (!front) throw new Error("docs/guide has no README.md");

  const summaries = new Map(
    [
      ...(front.body ?? "").matchAll(
        /^\| \[[^\]]+\]\(([a-z-]+)\.md\) \| (.+) \|$/gm,
      ),
    ].map(([, slug, cell]) => [slug, plain(cell ?? "")]),
  );
  const guide = (slug: string): Guide => {
    const entry = byId.get(slug);
    if (!entry)
      throw new Error(
        `guide.ts lists ${slug}, but docs/guide has no ${slug}.md`,
      );
    return {
      slug,
      title: titleOf(entry),
      href: guideHref(slug),
      summary: summaries.get(slug) ?? "",
      entry,
    };
  };
  const groups = sections.map(([name, slugs]) => ({
    name,
    guides: slugs.map(guide),
  }));
  const guides = groups.flatMap((group) => group.guides);

  const listed = new Set<string>(guides.map((g) => g.slug));
  const unlisted = entries.filter(
    (e) => e.id !== "readme" && !listed.has(e.id),
  );
  if (unlisted.length > 0)
    throw new Error(
      `add ${unlisted.map((e) => e.id).join(", ")} to a section in site/src/guide.ts`,
    );

  return { front, groups, guides };
}
