  // Errors and Web Vitals (ADR 0037, phase A2), spliced into s.js for
  // sites that collect them. At most 10 errors a page load, each message
  // once; the vitals of the page load once, when it is first hidden.
  var seen = {}, errs = 0;
  function err(t, m, s) {
    m = String(m || "").slice(0, 500);
    if (errs > 9 || seen[m]) return;
    seen[m] = 1;
    errs++;
    send(4, { n: m, t: String(t || "Error").slice(0, 80), s: String(s || "").slice(0, 4000) });
  }
  w.addEventListener("error", function (ev) {
    var e = ev.error;
    if (ev.message || e) err(e && e.name, (e && e.message) || ev.message, (e && e.stack) || (ev.filename ? "at " + ev.filename + ":" + ev.lineno + ":" + ev.colno : ""));
  });
  w.addEventListener("unhandledrejection", function (ev) {
    var r = ev.reason;
    if (r instanceof Error) return err(r.name, r.message, r.stack);
    try {
      r = typeof r === "string" ? r : JSON.stringify(r);
    } catch (x) {
      r = String(r);
    }
    err("rejection", r);
  });
  var p = w.performance, u = l.href, v = { lcp: -1, inp: -1, cls: -1, fcp: -1, ttfb: -1 }, sent, cw = 0, cf = 0, cl = 0;
  var nav = p && p.getEntriesByType && p.getEntriesByType("navigation")[0], act = (nav && nav.activationStart) || 0;
  if (nav) v.ttfb = Math.max(nav.responseStart - act, 0);
  function po(o, f) {
    try {
      new PerformanceObserver(function (list) { list.getEntries().forEach(f); }).observe(o);
      return 1;
    } catch (x) {}
  }
  po({ type: "paint", buffered: true }, function (e) {
    if (e.name === "first-contentful-paint") v.fcp = Math.max(e.startTime - act, 0);
  });
  po({ type: "largest-contentful-paint", buffered: true }, function (e) {
    v.lcp = Math.max(e.startTime - act, 0);
  });
  // CLS is the largest burst of shifts: each under a second from the
  // last, the burst under five seconds.
  if (po({ type: "layout-shift", buffered: true }, function (e) {
    if (e.hadRecentInput) return;
    if (cw && e.startTime - cl < 1000 && e.startTime - cf < 5000) cw += e.value;
    else {
      cw = e.value;
      cf = e.startTime;
    }
    cl = e.startTime;
    v.cls = Math.max(v.cls, cw);
  })) v.cls = Math.max(v.cls, 0);
  // INP is the slowest interaction: web-vitals' value up to 50 of them.
  function inp(e) {
    if (e.interactionId || e.entryType === "first-input") v.inp = Math.max(v.inp, e.duration);
  }
  po({ type: "event", buffered: true, durationThreshold: 40 }, inp);
  po({ type: "first-input", buffered: true }, inp);
  d.addEventListener("visibilitychange", function () {
    if (d.visibilityState === "hidden" && !sent && v.ttfb + v.fcp > -2) {
      sent = 1;
      send(5, { u: u, v: v });
    }
  });
