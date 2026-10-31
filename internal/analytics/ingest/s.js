// Dock Analytics (ADR 0037). Cookieless: nothing is stored in the browser.
// Page views, single-page app route changes, outbound link clicks and
// dock.track(name, props). Served by Dock at /_dock/a/s.js.
(function () {
  var w = window, d = document, l = location, n = navigator, last = "", first = true;
  if (w.dock && w.dock.track) return;
  function send(k, name, props) {
    var e = { k: k, u: l.href, w: screen.width, h: screen.height, l: n.language };
    if (name) e.n = name;
    if (props) e.p = props;
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
    if (a && a.host && a.host !== l.host) send(3, a.href);
  }, true);
  w.dock = { track: function (name, props) { send(2, String(name), props); } };
  if (d.visibilityState === "prerender") d.addEventListener("visibilitychange", page, { once: true });
  else page();
})();
