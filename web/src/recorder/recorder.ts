import { record } from "@rrweb/record";

// Dock Analytics' session recorder (ADR 0037, phase A3), served by every
// Dock server at /_dock/a/r.js and loaded by s.js only for the page loads
// a site samples. It is not part of Dock's UI: `pnpm build:recorder`
// bundles it into internal/analytics/ingest/r.js, which the ingest embeds.
//
// Each page load is a segment with a random ID: nothing is stored in the
// browser, and the store stitches a visitor's segments into sessions.
// Inputs are always masked, all text when the site asks (dock.r.m), and
// elements with data-dock-block are left out. Events go every few
// seconds as gzipped chunks; a page load stops recording past its byte
// budget or at a chunk too big to send.

/** What s.js hands the recorder: m masks all text. */
type Config = { m?: number };

const w = window as Window & { dock?: { r?: Config } };
const config = w.dock?.r ?? {};
const segment = Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) =>
  b.toString(16).padStart(2, "0"),
).join("");

/** The ingest's cap on a chunk's gzipped bytes (analytics.MaxReplayChunk). */
const maxChunk = 384 << 10;
/** What one page load may send in all, gzipped. */
const maxBytes = 8 << 20;
/** How often buffered events go, and how many make a chunk go at once. */
const every = 5000;
const maxEvents = 1000;

let events: { timestamp: number }[] = [];
let seq = 0;
let sent = 0;

/** gzip compresses s with the browser's CompressionStream. */
function gzip(s: string): Promise<Blob> {
  return new Response(
    new Blob([s]).stream().pipeThrough(new CompressionStream("gzip")),
  ).blob();
}

/** flush sends the buffered events as the next chunk. */
async function flush(hidden: boolean) {
  const chunk = events;
  if (chunk.length === 0 || sent > maxBytes) return;
  events = [];
  const first = chunk[0]?.timestamp ?? 0;
  const last = chunk[chunk.length - 1]?.timestamp ?? first;
  const q = new URLSearchParams({
    g: segment,
    q: String(seq++),
    a: String(first),
    b: String(last),
    c: String(Date.now()),
    n: String(chunk.length),
    u: location.href,
  });
  const body = await gzip(JSON.stringify(chunk));
  sent += body.size;
  if (body.size > maxChunk || sent > maxBytes) {
    stop?.();
    return;
  }
  // keepalive lets a hidden page's last chunk finish, up to the browser's
  // 64 KB in flight.
  fetch(`/_dock/a/r?${q}`, {
    method: "POST",
    body,
    keepalive: hidden && body.size < 60000,
  }).catch(() => {});
}

const stop = record({
  emit(e) {
    events.push(e);
    if (events.length >= maxEvents) void flush(false);
  },
  maskAllInputs: true,
  maskTextSelector: config.m ? "*" : undefined,
  blockSelector: "[data-dock-block]",
  sampling: { mousemove: 50, scroll: 150, input: "last" },
  recordCanvas: false,
  collectFonts: false,
  inlineImages: false,
});

const timer = setInterval(() => void flush(false), every);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") void flush(true);
});
addEventListener("pagehide", () => {
  clearInterval(timer);
  void flush(true);
});
