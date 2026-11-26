// Dock Analytics (ADR 0037). Cookieless: nothing is stored in the browser.
// Page views, single-page app route changes, outbound link clicks and
// dock.track(name, props). Served by Dock at /_dock/a/s.js with the site's
// settings as c, and with ev.js in place of the ev marker when the site
// collects errors and Web Vitals. A site that records sessions has c.r,
// the percent of page loads to record (c.m: mask all text); those load
// the recorder, /_dock/a/r.js.
//
// Consent (ADR 0085): c.c is what waits for dock.consent(true), 1 replay
// and 2 everything; without it everything runs at load. The page gives
// consent with dock.consent(true), before this script loads with
// window.dock = { consent: true }, or with data-consent="granted" on the
// script tag; dock.consent(false) withdraws it and stops the recorder.
//
// Dock's Caddy adds the tag to pages itself, marked data-dock-auto (ADR
// 0105). That tag takes itself out of the page, so a framework hydrating
// the whole document never meets it, and gives way to a tag the page has
// of its own, whose data-consent counts.
(function (c) {
  var w = window, d = document, l = location, n = navigator, last = "", first = true, rec, pre = w.dock && w.dock.consent;
  if (w.dock && w.dock.track) return;
  var tag = d.currentScript;
  if (tag && tag.hasAttribute("data-dock-auto")) {
    tag.remove();
    if (d.querySelector('script[src*="/_dock/a/s.js"]:not([data-dock-auto])')) return;
  }
  var ok = pre === true || (tag && tag.getAttribute("data-consent") === "granted") || (!c.c && pre !== false);
  var sampled = c.r && w.CompressionStream && Math.random() * 100 < c.r;
  function send(k, x) {
    if (c.c > 1 && !ok) return;
    var e = { k: k, u: l.href, w: screen.width, h: screen.height, l: n.language }, i;
    for (i in x) e[i] = x[i];
    if (k === 1 && first) {
      e.r = d.referrer;
      first = false;
    }
    var b = JSON.stringify(e);
    if (!(n.sendBeacon && n.sendBeacon("/_dock/a/e", b))) {
      fetch("/_dock/a/e", { method: "POST", body: b, keepalive: true }).catch(function () {});
    }
  }
  function page() {
    if (l.href === last) return;
    last = l.href;
    send(1);
  }
  var push = history.pushState;
  history.pushState = function () {
    push.apply(this, arguments);
    page();
  };
  w.addEventListener("popstate", page);
  d.addEventListener("click", function (ev) {
    var a = ev.target && ev.target.closest && ev.target.closest("a[href]");
    if (a && a.host && a.host !== l.host) send(3, { n: a.href });
  }, true);
  // record loads the recorder on a sampled page load once there is consent.
  function record() {
    if (!sampled || !ok || rec) return;
    rec = w.dock.r = { m: c.m };
    var r = d.createElement("script");
    r.src = "/_dock/a/r.js";
    r.async = true;
    (d.head || d.documentElement).appendChild(r);
  }
  w.dock = {
    track: function (name, props) { send(2, { n: String(name), p: props }); },
    consent: function (yes) {
      var was = ok;
      ok = !!yes;
      if (!ok && rec) {
        rec.off = 1;
        if (rec.stop) rec.stop();
      }
      if (ok && !was && c.c > 1) {
        last = "";
        page();
      }
      record();
    },
  };
  /*ev*/
  record();
  if (d.visibilityState === "prerender") d.addEventListener("visibilitychange", page, { once: true });
  else page();
})(/*c*/{});
