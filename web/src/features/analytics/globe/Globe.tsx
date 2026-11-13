import { type PointerEvent, useEffect, useMemo, useRef, useState } from "react";
import type { AnalyticsRow } from "../data";
import { count, country } from "../parts";
import { centres } from "./centres";
import { land } from "./land";
import { clampPitch, facing, landDots, turn, type View, vec } from "./sphere";

// Where a site's visitors are, as dots on a globe (Saile's pick,
// 2026-11-13): the land as a field of dots, a dot per country sized by
// its visitors. Canvas 2D and no library, loaded with the Globe tab
// (lazy). It never animates: it draws when its data or highlight change
// and while dragged or turned with the arrow keys, once per frame at most.

const dots = landDots(land);

/** Theme colours, read once: the canvas can't use Tailwind's classes. */
function palette() {
  const css = getComputedStyle(document.documentElement);
  const v = (name: string) => css.getPropertyValue(`--color-${name}`).trim();
  return {
    sphere: v("panel"),
    rim: v("line"),
    near: v("dash"),
    far: v("edge"),
    dot: v("info"),
    on: v("fg"),
  };
}

/** A view's pitch at 60%: the globe's curve shows above and below. */
const tipped = (v: View): View => ({ ...v, pitch: v.pitch * 0.6 });

export default function Globe({
  rows,
  hover,
  onHover,
  onPick,
}: {
  /** Countries by ISO code, with their visitors. */
  rows: AnalyticsRow[];
  /** The country highlighted, from here or the list beside it. */
  hover: string | null;
  onHover: (code: string | null) => void;
  /** A dot was clicked: filter by its country. */
  onPick: (row: AnalyticsRow) => void;
}) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const points = useMemo(() => {
    const most = Math.max(1, ...rows.map((r) => r.visitors));
    return rows.flatMap((row) => {
      const c = centres[row.value];
      return c
        ? [{ row, at: vec(c[0], c[1]), size: Math.sqrt(row.visitors / most) }]
        : [];
    });
  }, [rows]);
  const view = useRef<View | null>(null);
  // Faces the visitors when they first arrive, tipped towards the equator
  // so a northern audience doesn't put the pole in the middle; after that,
  // where it was turned.
  view.current ??= points.length
    ? tipped(facing(points.map((p) => ({ at: p.at, weight: p.row.visitors }))))
    : null;
  const [size, setSize] = useState(0);
  // What the last frame drew, for finding the dot under the pointer.
  const drawn = useRef<{ code: string; x: number; y: number; r: number }[]>([]);
  const queued = useRef(0);
  const colours = useMemo(palette, []);

  const draw = () => {
    queued.current = 0;
    const el = canvas.current;
    const g = el?.getContext("2d");
    if (!el || !g || !size) return;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    if (el.width !== size * dpr) {
      el.width = size * dpr;
      el.height = size * dpr;
    }
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, size, size);
    const r = size / 2 - 4;
    const c = size / 2;
    const v = view.current ?? { yaw: 0, pitch: 0.5 };
    g.beginPath();
    g.arc(c, c, r, 0, 2 * Math.PI);
    g.fillStyle = colours.sphere;
    g.fill();
    g.strokeStyle = colours.rim;
    g.lineWidth = 1;
    g.stroke();
    const dot = Math.max(1.2, size / 220);
    for (const d of dots) {
      const [x, y, z] = turn(d, v);
      if (z <= 0) continue;
      g.fillStyle = z > 0.25 ? colours.near : colours.far;
      g.fillRect(c + x * r - dot / 2, c - y * r - dot / 2, dot, dot);
    }
    const scale = size / 340;
    const seen = points
      .map((p) => {
        const [x, y, z] = turn(p.at, v);
        return {
          code: p.row.value,
          x: c + x * r,
          y: c - y * r,
          z,
          r: (2.5 + 11 * p.size) * scale,
        };
      })
      .filter((p) => p.z > 0.02)
      // Big dots first, so small ones on top stay visible.
      .sort((a, b) => b.r - a.r);
    for (const p of seen) {
      const on = p.code === hover;
      g.beginPath();
      g.arc(p.x, p.y, p.r, 0, 2 * Math.PI);
      g.globalAlpha = 0.28;
      g.fillStyle = colours.dot;
      g.fill();
      g.globalAlpha = 1;
      g.lineWidth = on ? 2 : 1;
      g.strokeStyle = on ? colours.on : colours.dot;
      g.stroke();
    }
    drawn.current = seen;
  };
  // The frame draws with the newest props, not those of the render that asked.
  const latest = useRef(draw);
  latest.current = draw;
  const later = () => {
    queued.current ||= requestAnimationFrame(() => latest.current());
  };
  // Redraw when the data, the highlight or the size change.
  useEffect(later);
  useEffect(() => () => cancelAnimationFrame(queued.current), []);

  // The canvas is as wide as its column, up to 340px: no listener while
  // the size holds still.
  useEffect(() => {
    const el = canvas.current?.parentElement;
    if (!el) return;
    const fit = () => setSize(Math.min(340, Math.floor(el.clientWidth)));
    fit();
    const watch = new ResizeObserver(fit);
    watch.observe(el);
    return () => watch.disconnect();
  }, []);

  const drag = useRef<{
    x: number;
    y: number;
    from: View;
    moved: boolean;
  } | null>(null);
  const hit = (e: PointerEvent) => {
    const box = e.currentTarget.getBoundingClientRect();
    const x = e.clientX - box.left;
    const y = e.clientY - box.top;
    // Topmost first: drawn last.
    return [...drawn.current]
      .reverse()
      .find((p) => (p.x - x) ** 2 + (p.y - y) ** 2 <= Math.max(36, p.r ** 2));
  };
  const turnBy = (dx: number, dy: number, from: View) => {
    view.current = {
      yaw: from.yaw + dx,
      pitch: clampPitch(from.pitch + dy),
    };
    later();
  };
  const shown = hover ? points.find((p) => p.row.value === hover) : undefined;

  return (
    <div className="flex w-full max-w-[340px] flex-col gap-1.5">
      <canvas
        ref={canvas}
        tabIndex={0}
        role="img"
        aria-label="Visitors by country on a globe. The arrow keys turn it; the list beside it has the same numbers."
        style={{ width: size, height: size }}
        className="cursor-grab touch-none rounded-full active:cursor-grabbing"
        onPointerDown={(e) => {
          e.currentTarget.setPointerCapture(e.pointerId);
          drag.current = {
            x: e.clientX,
            y: e.clientY,
            from: view.current ?? { yaw: 0, pitch: 0.5 },
            moved: false,
          };
        }}
        onPointerMove={(e) => {
          const d = drag.current;
          if (d) {
            const dx = e.clientX - d.x;
            const dy = e.clientY - d.y;
            d.moved ||= Math.abs(dx) + Math.abs(dy) > 3;
            if (d.moved) turnBy(dx * 0.006, dy * 0.006, d.from);
            return;
          }
          const code = hit(e)?.code ?? null;
          if (code !== hover) onHover(code);
        }}
        onPointerUp={(e) => {
          const d = drag.current;
          drag.current = null;
          if (d?.moved) return;
          const code = hit(e)?.code;
          const row = points.find((p) => p.row.value === code)?.row;
          if (row) onPick(row);
        }}
        onPointerCancel={() => {
          drag.current = null;
        }}
        onPointerLeave={() => {
          if (!drag.current && hover) onHover(null);
        }}
        onKeyDown={(e) => {
          const step = {
            ArrowLeft: [-0.17, 0],
            ArrowRight: [0.17, 0],
            ArrowUp: [0, 0.17],
            ArrowDown: [0, -0.17],
          }[e.key];
          if (!step) return;
          e.preventDefault();
          turnBy(
            step[0] ?? 0,
            step[1] ?? 0,
            view.current ?? { yaw: 0, pitch: 0.5 },
          );
        }}
      />
      <span className="min-h-[18px] text-xs text-faint">
        {shown
          ? `${country(shown.row.value)}: ${count(shown.row.visitors)} ${shown.row.visitors === 1 ? "visitor" : "visitors"}`
          : "Drag to turn it."}
      </span>
    </div>
  );
}
