// What a session recording keeps and drops (ADR 0037, "Masking").
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { recordOptions, scrubber, withoutQuery } from "./privacy.ts";

/** Every <input> type HTML has, and whether its value is a visitor's. */
const inputTypes = {
  button: false,
  checkbox: false,
  color: true,
  date: true,
  "datetime-local": true,
  email: true,
  file: true,
  hidden: true,
  image: false,
  month: true,
  number: true,
  password: true,
  radio: false,
  range: true,
  reset: false,
  search: true,
  submit: false,
  tel: true,
  text: true,
  time: true,
  url: true,
  week: true,
};

test("every field that holds a value is masked, hidden ones too", () => {
  const masked: Record<string, boolean> = recordOptions(false).maskInputOptions;
  for (const [type, holdsValue] of Object.entries(inputTypes)) {
    assert.equal(!!masked[type], holdsValue, `<input type=${type}>`);
  }
  assert.equal(masked.textarea, true);
  assert.equal(masked.select, true);
});

test("all text is masked only when the site asks", () => {
  assert.equal(recordOptions(false).maskTextSelector, undefined);
  assert.equal(recordOptions(true).maskTextSelector, "*");
});

test("an address loses its query and fragment", () => {
  assert.equal(
    withoutQuery("https://shop.example/reset?token=abc#top"),
    "https://shop.example/reset",
  );
  assert.equal(withoutQuery("/orders/12#access_token=abc"), "/orders/12");
  assert.equal(withoutQuery("/orders/12"), "/orders/12");
});

const element = (
  id: number,
  tagName: string,
  attributes: Record<string, unknown>,
  childNodes: unknown[] = [],
) => ({ type: 2, id, tagName, attributes, childNodes });

test("the page's address is recorded as its path", () => {
  const event = {
    type: 4,
    data: {
      href: "https://shop.example/reset?token=abc#x",
      width: 1,
      height: 1,
    },
  };
  scrubber()(event);
  assert.equal(event.data.href, "https://shop.example/reset");
});

test("a snapshot's links, form actions and meta tags lose what may be secret", () => {
  const link = element(4, "a", { href: "/account?session=abc#tab" });
  const form = element(5, "form", {
    action: "/reset?token=abc",
    method: "post",
  });
  const csrf = element(6, "meta", { name: "csrf-token", content: "abc" });
  const viewport = element(7, "meta", {
    name: "viewport",
    content: "width=device-width",
  });
  const charset = element(8, "meta", {
    "http-equiv": "Content-Type",
    content: "text/html; charset=utf-8",
  });
  const sheet = element(9, "link", { rel: "stylesheet", href: "/app.css?v=3" });
  const image = element(10, "img", { src: "/logo.png?v=3" });
  const event = {
    type: 2,
    data: {
      node: {
        type: 0,
        id: 1,
        childNodes: [
          element(2, "html", {}, [
            element(3, "head", {}, [csrf, viewport, charset, sheet]),
            element(11, "body", {}, [link, form, image]),
          ]),
        ],
      },
    },
  };
  scrubber()(event);
  assert.equal(link.attributes.href, "/account");
  assert.deepEqual(form.attributes, { action: "/reset", method: "post" });
  assert.equal(csrf.attributes.content, "");
  assert.equal(viewport.attributes.content, "width=device-width");
  assert.equal(charset.attributes.content, "text/html; charset=utf-8");
  // A replay loads these, so they stay whole.
  assert.equal(sheet.attributes.href, "/app.css?v=3");
  assert.equal(image.attributes.src, "/logo.png?v=3");
});

test("nodes added later and attribute changes are scrubbed like the snapshot", () => {
  const scrub = scrubber();
  const link = element(20, "a", { href: "/invite?code=abc" });
  const sheet = element(21, "link", {
    rel: "stylesheet",
    href: "/late.css?v=1",
  });
  scrub({
    type: 3,
    data: {
      source: 0,
      adds: [
        { parentId: 11, nextId: null, node: link },
        { parentId: 3, nextId: null, node: sheet },
      ],
      attributes: [],
    },
  });
  assert.equal(link.attributes.href, "/invite");

  const changes = [
    { id: 20, attributes: { href: "/invite?code=def#x", class: "on" } },
    { id: 21, attributes: { href: "/late.css?v=2" } },
  ];
  scrub({ type: 3, data: { source: 0, adds: [], attributes: changes } });
  assert.deepEqual(changes[0]?.attributes, { href: "/invite", class: "on" });
  assert.equal(changes[1]?.attributes.href, "/late.css?v=2");
});

test("other events pass through untouched", () => {
  const scroll = { type: 3, data: { source: 3, id: 20, x: 0, y: 40 } };
  const before = structuredClone(scroll);
  scrubber()(scroll);
  assert.deepEqual(scroll, before);
});
