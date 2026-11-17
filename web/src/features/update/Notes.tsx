import type { ReactNode } from "react";

// Release notes are Markdown from the release source. GitHub's generated
// notes only use a small part of it, so this renders that part as React
// elements (never as HTML): headings, list items, paragraphs, **bold**,
// `code`, [links](https://...) and bare https links. Anything else shows
// as the text it is. `headlines` boils the same notes down to a line or
// two per section for the Updates page; `Notes` shows them in full.

/** Release notes as headings, lists and paragraphs. */
export function Notes({ text }: { text: string }) {
  const blocks: ReactNode[] = [];
  let items: string[] = [];
  let para: string[] = [];
  const flush = () => {
    if (items.length) {
      blocks.push(
        <ul key={blocks.length} className="my-1 list-disc pl-5 text-sec">
          {items.map((item, i) => (
            <li key={i}>{inline(item)}</li>
          ))}
        </ul>,
      );
      items = [];
    }
    if (para.length) {
      blocks.push(
        <p key={blocks.length} className="my-1 text-sec">
          {inline(para.join(" "))}
        </p>,
      );
      para = [];
    }
  };
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    const heading = /^#{1,6}\s+(.*)$/.exec(line);
    const item = /^[-*+]\s+(.*)$/.exec(line);
    if (heading) {
      flush();
      blocks.push(
        <h4
          key={blocks.length}
          className="mt-3 mb-1 text-xs font-medium text-mut"
        >
          {inline(heading[1] ?? "")}
        </h4>,
      );
    } else if (item) {
      if (para.length) flush();
      items.push(item[1] ?? "");
    } else if (line === "") {
      flush();
    } else {
      if (items.length) flush();
      para.push(line);
    }
  }
  flush();
  return <div className="text-xs">{blocks}</div>;
}

/** One section of a release's notes: its heading and its headlines. */
export type Headlines = { title: string; lines: string[] };

/**
 * The notes' sections with a headline per top-level item: its bold lead
 * where the section's items have one ("**Every release is signed.** ..."
 * gives "Every release is signed"), else its first sentence; a section
 * without items gives its first sentence. Sub-items, ADR and pull request
 * references and long hex keys are left out. Lines keep their `code`,
 * for `Inline`.
 */
export function headlines(text: string): Headlines[] {
  type Section = {
    title: string;
    items: { lead?: string; sentence: string }[];
    para?: string;
  };
  const sections: Section[] = [];
  let cur: Section | undefined;
  for (const raw of text.split("\n")) {
    const heading = /^#{1,6}\s+(.*)$/.exec(raw.trim());
    if (heading) {
      cur = { title: tidy(heading[1] ?? ""), items: [] };
      sections.push(cur);
      continue;
    }
    if (!raw.trim() || /^\s/.test(raw)) continue; // blank, or nested
    if (!cur) {
      cur = { title: "", items: [] };
      sections.push(cur);
    }
    const item = /^(?:[-*+]|\d+\.)\s+(.*)$/.exec(raw);
    if (item) {
      const body = item[1] ?? "";
      const lead = /^\*\*(.+?)\*\*/.exec(body)?.[1];
      cur.items.push({
        lead: lead && tidy(lead),
        sentence: tidy(body.split(/(?<=[.;])\s/)[0] ?? body),
      });
    } else cur.para ??= tidy(raw.split(/(?<=[.;])\s/)[0] ?? raw);
  }
  return sections
    .map(({ title, items, para }) => ({
      title,
      lines: items.some((i) => i.lead)
        ? items.flatMap((i) => i.lead ?? [])
        : items.length
          ? items.map((i) => i.sentence)
          : para
            ? [para]
            : [],
    }))
    .filter((s) => s.lines.length > 0);
}

/** A headline without references, keys, bold marks or end punctuation. */
const tidy = (s: string) =>
  s
    .replace(/\s*\((?:ADR|#)[^)]*\)/g, "")
    .replace(/\b[0-9a-f]{32,}\b/g, "")
    .replace(/\*\*/g, "")
    .trim()
    .replace(/[.:;,]+$/, "");

/**
 * One line of Markdown: bold, code and links as elements. `plain` keeps
 * links as their words, for a line inside a <summary>, which may hold
 * no other control.
 */
export function Inline({ text, plain }: { text: string; plain?: boolean }) {
  return <>{inline(text, plain)}</>;
}

const token =
  /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s)]+)/g;

/** Bold, code and links in one line (links as words when `plain`); the rest is text. */
function inline(s: string, plain = false): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of s.matchAll(token)) {
    const i = m.index ?? 0;
    if (i > last) out.push(s.slice(last, i));
    const [, bold, code, label, href, bare] = m;
    // Bold may hold code: **`[updates] trusted_keys`**.
    if (bold) out.push(<strong key={i}>{inline(bold, plain)}</strong>);
    else if (code) out.push(<code key={i}>{code}</code>);
    else if (plain) out.push(label ?? shortLink(href ?? bare ?? ""));
    else {
      const url = href ?? bare ?? "";
      out.push(
        <a
          key={i}
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-info"
        >
          {label ?? shortLink(url)}
        </a>,
      );
    }
    last = i + m[0].length;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
}

/** GitHub pull request and compare links as "#12" and "v0.2.0...v0.3.0". */
function shortLink(url: string) {
  const pr = /\/pull\/(\d+)$/.exec(url);
  if (pr) return `#${pr[1]}`;
  const cmp = /\/compare\/(.+)$/.exec(url);
  return cmp ? cmp[1] : url;
}
