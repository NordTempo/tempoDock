import { defineMdastPlugin } from "satteri";
import { siteLink } from "./guide";

/* Markdown plugins for the guide's pages (astro.config.ts). */

/** Text as HTML text. */
const escapeHtml = (text: string) =>
  text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/** Links between guide pages and out to the repository (siteLink). */
export const guideLinks = defineMdastPlugin({
  name: "guide-links",
  link(node, ctx) {
    const url = siteLink(node.url);
    if (url !== node.url) ctx.setField(node, "url", url);
  },
});

/** One part of a shell block: a comment's words, or a command to copy. */
type Part = { note: string } | { command: string };

/**
 * Splits a shell block's lines into notes and commands. A command takes
 * the lines that continue it (after a trailing backslash, or indented);
 * comment lines in a row are one note. Returns nothing for a script
 * (a shebang, loops and conditions), which is shown whole.
 */
function shellParts(source: string): Part[] | undefined {
  if (/^(#!|(done|fi|esac|\})\b)/m.test(source)) return undefined;
  const parts: Part[] = [];
  let continues = false;
  for (const line of source.split("\n")) {
    const last = parts.at(-1);
    if (last && "command" in last && (continues || /^\s+\S/.test(line))) {
      last.command += `\n${line}`;
    } else if (line.startsWith("#")) {
      const words = line.replace(/^#+\s*/, "");
      if (last && "note" in last) last.note += ` ${words}`;
      else parts.push({ note: words });
    } else if (line.trim()) {
      parts.push({ command: line });
    }
    continues = line.endsWith("\\");
  }
  return parts;
}

/**
 * Shell blocks become a list of commands, each one selected whole by a
 * click (CSS user-select: all, so no script), with the block's comments
 * as the words between them.
 */
export const shellCommands = defineMdastPlugin({
  name: "shell-commands",
  code(node) {
    if (!["sh", "bash", "shell"].includes(node.lang ?? "")) return;
    const parts = shellParts(node.value);
    if (!parts) return;
    const rows = parts.map((part) =>
      "note" in part
        ? `<p>${escapeHtml(part.note).replace(/`([^`]+)`/g, "<code>$1</code>")}</p>`
        : `<pre><code>${escapeHtml(part.command)}</code></pre>`,
    );
    return { rawHtml: `<div class="commands">${rows.join("")}</div>` };
  },
});
