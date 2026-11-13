// Open /tests/not-live.html under Vite at a few widths (320, 390, 768,
// 1280); no backend. The real shell over a fetch fixture and a fake live
// stream: a drop shorter than NOT_LIVE_DELAY shows nothing, a longer one
// shows the strip under the bar (top bar or console bar) on a scrolled
// page, covering no control there and pushing the sticky page bar down,
// and the stream coming back clears it.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { createRoot } from "react-dom/client";
import type { Org, Session } from "../data/types";
import "../index.css";

/** Fails both the browser title and console for automation. */
function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}
window.addEventListener("unhandledrejection", (event) => {
  document.title = `FAIL: ${String(event.reason)}`;
});
// A failed check at the top level arrives here.
window.addEventListener("error", (event) => {
  document.title = `FAIL: ${event.message}`;
});
const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms));
const frame = () => new Promise((done) => requestAnimationFrame(done));

const orgs: Org[] = [
  {
    slug: "a",
    name: "A",
    role: "admin",
    require_two_factor: false,
    created_at: "2026-11-13T10:00:00Z",
  },
];
window.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(input);
  const path = new URL(request.url).pathname;
  if (path === "/api/v1/me") {
    const me: Session = {
      kind: "user",
      name: "saile",
      operator: true,
      org: "a",
    };
    return Response.json(me);
  }
  if (path === "/api/v1/orgs") return Response.json(orgs);
  return Response.json([]);
};

/** The event bus streams the shell opened, newest last. */
const streams: FakeSource[] = [];
/** EventSource without a server: the test opens, drops and closes it. */
class FakeSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = 0;
  readonly url: string;
  constructor(url: string) {
    super();
    this.url = url;
    if (url.includes("/api/v1/events")) streams.push(this);
    setTimeout(() => this.open(), 0);
  }
  open() {
    this.readyState = 1;
    this.dispatchEvent(new Event("open"));
    this.dispatchEvent(new MessageEvent("ready", { data: "{}" }));
  }
  /** A lost connection the browser retries. */
  drop() {
    this.readyState = 0;
    this.dispatchEvent(new Event("error"));
  }
  /** A refused one: the browser gives up. */
  refuse() {
    this.readyState = 2;
    this.dispatchEvent(new Event("error"));
  }
  close() {
    this.readyState = 2;
  }
}
Object.defineProperty(window, "EventSource", { value: FakeSource });

const [{ setOrg }, { routeTree }, { NOT_LIVE_DELAY }] = await Promise.all([
  import("../api/client"),
  import("../routeTree.gen"),
  import("./NotLive"),
]);
setOrg("a");
const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
});
const router = createRouter({
  routeTree,
  context: { queryClient },
  history: createMemoryHistory({ initialEntries: ["/account"] }),
});
const root = document.getElementById("root");
check(!!root, "#root is missing");
if (root)
  createRoot(root).render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );

const strip = () => document.querySelector<HTMLElement>(".not-live");
const stream = () => {
  const last = streams.at(-1);
  if (!last) throw new Error("The shell opened no live stream");
  return last;
};
async function until(what: string, ok: () => boolean) {
  for (let i = 0; i < 100 && !ok(); i++) await sleep(20);
  check(ok(), `Timed out waiting for ${what}`);
}

/**
 * The strip sits right under `bar`, at its full 28px with its words
 * whole, every control in the bar is still the topmost element at its
 * centre, and the page's sticky bar starts below the strip.
 */
function checkLayout(bar: HTMLElement, where: string) {
  const s = strip();
  check(!!s, `${where}: the strip must show`);
  if (!s) return;
  const sr = s.getBoundingClientRect();
  const br = bar.getBoundingClientRect();
  check(br.top === 0, `${where}: the bar must stay at the top, at ${br.top}`);
  check(
    Math.abs(sr.top - br.bottom) < 1,
    `${where}: the strip must start under the bar (${sr.top} vs ${br.bottom})`,
  );
  check(
    sr.height === 28,
    `${where}: the strip must be one 28px line, ${sr.height}`,
  );
  const words = s.querySelector("span");
  check(
    !!words && words.scrollWidth <= words.clientWidth,
    `${where}: the strip's words must fit at ${innerWidth}px`,
  );
  for (const control of bar.querySelectorAll<HTMLElement>("a, button")) {
    const r = control.getBoundingClientRect();
    if (!r.width || r.right > innerWidth) continue;
    const top = document.elementFromPoint(
      r.left + r.width / 2,
      r.top + r.height / 2,
    );
    check(
      !!top && control.contains(top),
      `${where}: "${control.textContent}" must not be covered`,
    );
  }
  const pageBar = document.querySelector<HTMLElement>("#main header");
  if (pageBar)
    check(
      pageBar.getBoundingClientRect().top >= sr.bottom - 1,
      `${where}: the page bar must sit under the strip`,
    );
}

/** Drops the stream on a scrolled page and checks the strip end to end. */
async function run(bar: () => HTMLElement | null, where: string) {
  await until(`${where}'s bar`, () => !!bar());
  await until(`${where}'s live stream`, () => stream().readyState === 1);
  scrollTo(0, 600);

  // A blip: back before the delay, nothing shows.
  stream().drop();
  await sleep(NOT_LIVE_DELAY / 2);
  check(!strip(), `${where}: a drop must not show before ${NOT_LIVE_DELAY}ms`);
  stream().open();
  await sleep(NOT_LIVE_DELAY);
  check(!strip(), `${where}: a blip must never show the strip`);

  // A real drop: the strip, under the bar.
  stream().drop();
  await sleep(NOT_LIVE_DELAY + 100);
  await frame();
  const b = bar();
  if (b) checkLayout(b, `${where}, retrying`);
  stream().open();
  await frame();
  check(!strip(), `${where}: the strip must clear when the stream is back`);

  // Refused: Reconnect opens a new stream, which clears it.
  stream().refuse();
  await sleep(NOT_LIVE_DELAY + 100);
  await frame();
  if (b) checkLayout(b, `${where}, closed`);
  const before = streams.length;
  strip()?.querySelector("button")?.click();
  await until(`${where}'s new stream`, () => streams.length > before);
  await until(`${where}'s strip to clear`, () => !strip());
}

// The bar the strip goes under: first in the shell's sticky stack.
const bar = () =>
  document.querySelector<HTMLElement>(".sticky.top-0.z-30 > :first-child");
await run(bar, "Org shell");
check(
  !!document.querySelector(".app-shell"),
  "The org shell must have its top bar",
);
// Users reads a list, which the fetch fixture answers.
await router.navigate({ to: "/console/users" });
await until("the console bar", () => !document.querySelector(".app-shell"));
await run(bar, "Console");

document.title = "PASS: not live";
const results = document.getElementById("results");
if (results)
  results.textContent = `PASS at ${innerWidth}px: a blip shows nothing, a drop shows the strip under the bar without covering it, and it clears when the stream is back`;
console.log(document.title);
