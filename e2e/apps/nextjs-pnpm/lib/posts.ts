import { z } from "zod";

const Post = z.object({ slug: z.string(), title: z.string(), date: z.coerce.date(), body: z.string() });
export type Post = z.infer<typeof Post>;

// Posts generated at build time, so `next build` prerenders a few dozen pages.
export const posts: Post[] = Array.from({ length: 40 }, (_, i) =>
  Post.parse({ slug: `post-${i}`, title: `Post ${i}`, date: `2026-01-${String((i % 28) + 1).padStart(2, "0")}`, body: "Lorem ipsum ".repeat(50 + i) }),
);
