import type { ReactNode } from "react";

// Release notes are Markdown from the release source. GitHub's generated
// notes only use a small part of it, so this renders that part as React
// elements (never as HTML): headings, list items, paragraphs, **bold**,
// `code`, [links](https://...) and bare https links. Anything else shows
// as the text it is.

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

const token =
  /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s)]+)/g;

/** Bold, code and links in one line; the rest is text. */
function inline(s: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of s.matchAll(token)) {
    const i = m.index ?? 0;
    if (i > last) out.push(s.slice(last, i));
    const [, bold, code, label, href, bare] = m;
    if (bold) out.push(<strong key={i}>{bold}</strong>);
    else if (code) out.push(<code key={i}>{code}</code>);
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
