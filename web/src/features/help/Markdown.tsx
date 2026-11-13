import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useOrg } from "../../shell/location";

/**
 * The guide's Markdown as React: headings (with anchors), paragraphs,
 * lists, tables, code blocks, quotes, and inline code, bold, italics and
 * links. That is all docs/guide uses, so no library. Links to other
 * guide pages stay in the app; links outside docs/guide (ADRs, the
 * repository) have no page here and become their text.
 */
export function Markdown({ source, page }: { source: string; page: string }) {
  return (
    <div className="prose flex flex-col gap-3">{blocks(source, page)}</div>
  );
}

/**
 * A heading's anchor, the way GitHub makes it and docs/guide's test
 * checks it (slugOf): lower case, only letters, digits, spaces, _ and -,
 * spaces as hyphens.
 */
export function slug(heading: string) {
  return heading
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9 _-]/g, "")
    .replaceAll(" ", "-");
}

const listItem = /^(\s*)(?:[-*]|\d+\.)\s+(.*)$/;
const tableRule = /^\|?[\s:|-]+\|?$/;

/** Splits the source into blocks and renders each. */
function blocks(source: string, page: string): ReactNode[] {
  const lines = source.split("\n");
  const out: ReactNode[] = [];
  const seen = new Map<string, number>();
  const inline = (text: string) => inlines(text, page);
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? "";
    const key = out.length;
    if (line.trim() === "") {
      i++;
    } else if (line.startsWith("```")) {
      const code: string[] = [];
      for (i++; i < lines.length && !lines[i]?.startsWith("```"); i++)
        code.push(lines[i] ?? "");
      i++;
      out.push(
        <pre
          key={key}
          className="overflow-x-auto rounded-panel border border-line bg-panel px-3.5 py-2.5 font-mono text-xs text-sec"
        >
          <code>{code.join("\n")}</code>
        </pre>,
      );
    } else if (/^#{1,6}\s/.test(line)) {
      const level = line.indexOf(" ");
      const text = line.slice(level + 1);
      // A repeated heading gets -1, -2, ... after its anchor, like GitHub.
      let id = slug(text);
      const n = seen.get(id) ?? 0;
      seen.set(id, n + 1);
      if (n > 0) id += `-${n}`;
      out.push(<Heading key={key} level={level} id={id} text={inline(text)} />);
      i++;
    } else if (line.startsWith("|") && tableRule.test(lines[i + 1] ?? "")) {
      const head = cells(line);
      const rows: string[][] = [];
      for (i += 2; i < lines.length && lines[i]?.startsWith("|"); i++)
        rows.push(cells(lines[i] ?? ""));
      out.push(
        <div key={key} className="overflow-x-auto">
          <table>
            <thead>
              <tr>
                {head.map((c, j) => (
                  <th key={j}>{inline(c)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, r) => (
                <tr key={r}>
                  {row.map((c, j) => (
                    <td key={j} className="text-sec">
                      {inline(c)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
    } else if (listItem.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: { indent: number; text: string }[] = [];
      for (; i < lines.length; i++) {
        const l = lines[i] ?? "";
        const m = listItem.exec(l);
        if (m) items.push({ indent: m[1]?.length ?? 0, text: m[2] ?? "" });
        else if (l.trim() !== "" && /^\s/.test(l) && items.length > 0) {
          const last = items[items.length - 1];
          if (last) last.text += ` ${l.trim()}`;
        } else break;
      }
      const List = ordered ? "ol" : "ul";
      out.push(
        <List
          key={key}
          className={`flex flex-col gap-1 pl-5 text-sec ${ordered ? "list-decimal" : "list-disc"}`}
        >
          {items.map((item, j) => (
            <li
              key={j}
              style={item.indent ? { marginLeft: item.indent * 6 } : undefined}
            >
              {inline(item.text)}
            </li>
          ))}
        </List>,
      );
    } else if (line.startsWith(">")) {
      const quote: string[] = [];
      for (; i < lines.length && lines[i]?.startsWith(">"); i++)
        quote.push((lines[i] ?? "").replace(/^>\s?/, ""));
      out.push(
        <blockquote key={key} className="border-l-2 border-edge pl-3 text-sec">
          {inline(quote.join(" "))}
        </blockquote>,
      );
    } else {
      const text: string[] = [];
      for (; i < lines.length; i++) {
        const l = lines[i] ?? "";
        if (
          l.trim() === "" ||
          /^(#{1,6}\s|```|>|\|)/.test(l) ||
          listItem.test(l)
        )
          break;
        text.push(l.trim());
      }
      out.push(
        <p key={key} className="text-sec">
          {inline(text.join(" "))}
        </p>,
      );
    }
  }
  return out;
}

/** A table row's cells; `\|` inside one is a literal bar. */
function cells(row: string) {
  return row
    .replace(/^\|/, "")
    .replace(/\|\s*$/, "")
    .split(/(?<!\\)\|/)
    .map((c) => c.trim().replaceAll("\\|", "|"));
}

/** A heading at its level, with its anchor as id. */
function Heading({
  level,
  id,
  text,
}: {
  level: number;
  id: string;
  text: ReactNode;
}) {
  if (level === 1)
    return (
      <h1 id={id} className="text-xl font-semibold">
        {text}
      </h1>
    );
  if (level === 2)
    return (
      <h2
        id={id}
        className="mt-4 border-t border-line pt-4 text-md font-semibold"
      >
        {text}
      </h2>
    );
  return (
    <h3 id={id} className="mt-2 font-semibold">
      {text}
    </h3>
  );
}

const inlineToken =
  /`([^`]+)`|\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)\s]+)\)|(?<![\w*])\*([^*\s][^*]*)\*/g;

/** Inline code, bold, italics and links in a line of text. */
function inlines(text: string, page: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(inlineToken)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const key = at;
    const [, code, bold, label, href, em] = m;
    if (code !== undefined)
      out.push(
        <code
          key={key}
          className="rounded-tag bg-panel px-1 font-mono text-xs text-fg"
        >
          {code}
        </code>,
      );
    else if (bold !== undefined)
      out.push(
        <strong key={key} className="font-semibold text-fg">
          {inlines(bold, page)}
        </strong>,
      );
    else if (label !== undefined && href !== undefined)
      out.push(
        <GuideLink key={key} href={href} page={page}>
          {inlines(label, page)}
        </GuideLink>,
      );
    else if (em !== undefined) out.push(<em key={key}>{em}</em>);
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/**
 * A link in the guide: the web for http(s), the guide reader for another
 * page ("domains.md#tls") or a heading of this one ("#tls"), and plain
 * text for anything outside docs/guide.
 */
function GuideLink({
  href,
  page,
  children,
}: {
  href: string;
  page: string;
  children: ReactNode;
}) {
  const org = useOrg();
  if (/^https?:\/\//.test(href))
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    );
  const local = /^(?:([A-Za-z0-9-]+)\.md)?(?:#(.+))?$/.exec(href);
  if (!local || (!local[1] && !local[2])) return <>{children}</>;
  return (
    <Link
      to="/$org/help/guide/$page"
      params={{ org, page: local[1] ?? page }}
      hash={local[2]}
    >
      {children}
    </Link>
  );
}
