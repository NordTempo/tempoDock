// What a session recording leaves out (ADR 0037, "Masking"): the values
// of form fields, and what a page's own markup can carry of a visitor's
// secrets: the query and fragment of the page's address, of links and of
// form actions, and the content of <meta> tags. The recorder runs every
// event through a scrubber before it is buffered, so none of it leaves
// the browser.

/**
 * The field types whose values rrweb records as asterisks: every <input>
 * type that holds a value, hidden ones too (CSRF and reset tokens live
 * there), <textarea> and <select>. Buttons keep their labels, and a
 * checkbox or radio button whether it is ticked.
 */
export const maskedInputs = {
  color: true,
  date: true,
  "datetime-local": true,
  email: true,
  file: true,
  hidden: true,
  month: true,
  number: true,
  password: true,
  range: true,
  search: true,
  tel: true,
  text: true,
  time: true,
  url: true,
  week: true,
  textarea: true,
  select: true,
};

/**
 * What the recorder asks of rrweb, all but where events go: fields
 * masked (maskedInputs), all text too when the site asks (maskText),
 * elements with data-dock-block left out, and no canvases, fonts or
 * images inlined.
 */
export const recordOptions = (maskText: boolean) => ({
  maskInputOptions: maskedInputs,
  maskTextSelector: maskText ? "*" : undefined,
  blockSelector: "[data-dock-block]",
  sampling: { mousemove: 50, scroll: 150, input: "last" as const },
  recordCanvas: false,
  collectFonts: false,
  inlineImages: false,
});

/** url without its query and fragment. */
export function withoutQuery(url: string) {
  const at = url.search(/[?#]/);
  return at < 0 ? url : url.slice(0, at);
}

/** The <meta> names a replay needs to look right; the rest lose their content. */
const keptMeta = new Set(["viewport", "theme-color", "color-scheme"]);

/** An rrweb event, as far as the scrubber reads it. */
type Recorded = { type: number; data?: unknown };

/** A serialized DOM node: only elements (type 2) have a tag and attributes. */
type Node = {
  id?: number;
  type?: number;
  tagName?: string;
  attributes?: Record<string, unknown>;
  childNodes?: Node[];
};

/** A mutation's added nodes and changed attributes. */
type Mutation = {
  source?: number;
  adds?: { node?: Node }[];
  attributes?: { id: number; attributes?: Record<string, unknown> }[];
};

/** rrweb's event types and the mutation source, as its events number them. */
const fullSnapshot = 2;
const incremental = 3;
const meta = 4;
const mutation = 0;

/** The attribute an element loses (part of), if any. */
function scrubbed(node: Node): "href" | "action" | "content" | undefined {
  switch (node.tagName?.toLowerCase()) {
    case "a":
      return "href";
    case "form":
      return "action";
    case "meta": {
      const a = node.attributes ?? {};
      const name = String(a.name ?? "").toLowerCase();
      const equiv = String(a["http-equiv"] ?? "").toLowerCase();
      return keptMeta.has(name) || equiv === "content-type"
        ? undefined
        : "content";
    }
  }
  return undefined;
}

/** attrs without what attr may not carry: a URL's query, or all of a meta's content. */
function clean(attrs: Record<string, unknown> | undefined, attr: string) {
  const value = attrs?.[attr];
  if (!attrs || typeof value !== "string") return;
  attrs[attr] = attr === "content" ? "" : withoutQuery(value);
}

/**
 * A scrubber for one page load's events, changing them in place:
 *
 * - the page's address (rrweb's meta event) loses its query and fragment;
 * - <a href> and <form action> lose theirs, in the first snapshot, in
 *   nodes added later and when the attribute changes;
 * - <meta> tags lose their content, except the ones a replay needs to
 *   look right (viewport, theme-color, color-scheme, the charset).
 *
 * Stylesheet, image and script addresses stay whole: a replay loads them.
 * It remembers which nodes it scrubbed (an attribute change names only
 * the node's id), at most one entry per link, form and meta tag of the
 * page load.
 */
export function scrubber() {
  const marked = new Map<number, "href" | "action" | "content">();
  const walk = (node: Node | undefined) => {
    if (!node) return;
    if (node.type === 2) {
      const attr = scrubbed(node);
      if (attr) {
        if (node.id !== undefined) marked.set(node.id, attr);
        clean(node.attributes, attr);
      }
    }
    for (const child of node.childNodes ?? []) walk(child);
  };
  return (event: Recorded) => {
    if (typeof event.data !== "object" || event.data === null) return;
    if (event.type === meta) {
      const d = event.data as { href?: unknown };
      if (typeof d.href === "string") d.href = withoutQuery(d.href);
    } else if (event.type === fullSnapshot) {
      walk((event.data as { node?: Node }).node);
    } else if (event.type === incremental) {
      const d = event.data as Mutation;
      if (d.source !== mutation) return;
      for (const add of d.adds ?? []) walk(add.node);
      for (const change of d.attributes ?? []) {
        const attr = marked.get(change.id);
        if (attr) clean(change.attributes, attr);
      }
    }
  };
}
