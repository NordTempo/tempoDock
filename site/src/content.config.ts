import { defineCollection } from "astro:content";
import { glob } from "astro/loaders";
import { guideDir } from "./guide";

/**
 * The user guide: the repository's docs/guide pages, read where they are.
 * There is no copy under site/.
 */
export const collections = {
  guide: defineCollection({
    loader: glob({ pattern: "*.md", base: guideDir }),
  }),
};
