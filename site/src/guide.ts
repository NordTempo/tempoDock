import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { site } from "./site";

/**
 * Where the user guide's Markdown is: docs/guide in the repository, the one
 * source the docs pages render (content.config.ts). The image build has
 * only site/ as its context, so the Dockerfile fetches the directory and
 * names it in GUIDE_DIR.
 */
export const guideDir = resolve(process.env["GUIDE_DIR"] ?? "../docs/guide");

/** The guide's place in the repository, for links out to GitHub. */
const guideRepoPath = "docs/guide/";

/**
 * The docs' order: sections and the guides in each, by file name. A guide
 * in docs/guide that isn't here fails the build (docs.ts), so a new page
 * gets a place.
 */
export const sections = [
  ["Start", ["install", "getting-started"]],
  [
    "Deploy",
    [
      "deploying",
      "variables",
      "domains",
      "previews",
      "templates",
      "github-actions",
    ],
  ],
  ["Data", ["databases", "data"]],
  ["Watch", ["watching", "tracing", "status-pages"]],
  ["Team", ["organizations", "two-factor", "sso", "email"]],
  ["Automate", ["agents", "api"]],
] as const;

/** The page a guide's file is served at: README.md is the docs' front page. */
export const guideHref = (slug: string) =>
  slug === "readme" ? "/docs" : `/docs/${slug}`;

/** A guide file's source on GitHub. */
export const sourceUrl = (file: string) =>
  `${site.repoUrl}/blob/main/${guideRepoPath}${file}`;

/**
 * Turns a link written in a guide page into one that works on the site:
 * another guide page becomes its docs page, anything else in the
 * repository (ADRs, other docs) goes to GitHub. Links with a scheme and
 * links within the page stay as they are. A link to a guide page that
 * doesn't exist throws, which fails the build.
 */
export function siteLink(url: string): string {
  if (url.startsWith("#") || /^[a-z][a-z0-9+.-]*:/i.test(url)) return url;
  const target = new URL(url, `https://repo/${guideRepoPath}`);
  const path = target.pathname.slice(1);
  const page = /^docs\/guide\/([^/]+)\.md$/.exec(path)?.[1];
  if (!page) return `${site.repoUrl}/blob/main/${path}${target.hash}`;
  if (!existsSync(join(guideDir, `${page}.md`)))
    throw new Error(`the guide links to ${url}, which doesn't exist`);
  return guideHref(page.toLowerCase()) + target.hash;
}
