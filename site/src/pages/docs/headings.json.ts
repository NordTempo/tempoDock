import { render } from "astro:content";
import type { APIRoute } from "astro";
import { loadDocs } from "../../docs";

/**
 * The heading search's index: every guide's title and its second and
 * third level headings, with where each one is. Built with the site and
 * fetched by DocsSearch on first use.
 */
export const GET: APIRoute = async () => {
  const { guides } = await loadDocs();
  const index = await Promise.all(
    guides.map(async (guide) => {
      const { headings } = await render(guide.entry);
      return [
        { text: guide.title, guide: "Guide", href: guide.href },
        ...headings
          .filter((h) => h.depth === 2 || h.depth === 3)
          .map((h) => ({
            text: h.text,
            guide: guide.title,
            href: `${guide.href}#${h.slug}`,
          })),
      ];
    }),
  );
  return Response.json(index.flat());
};
