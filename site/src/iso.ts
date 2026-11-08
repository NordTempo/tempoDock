/**
 * Isometric dot planes, the illustration style of the whole site. A plane is
 * a square of `size` units seen from above at 30°, filled with an n×n grid of
 * small diamonds whose brightness a pattern picks. Everything here runs at
 * build time; the page gets plain SVG paths.
 */

/** How bright one grid cell is drawn: off, faint, normal or highlighted. */
export type Level = 0 | 1 | 2 | 3;

/** Picks a level for grid cell (i, j), i along the plane's x axis. */
export type Pattern = (i: number, j: number) => Level;

export type Point = readonly [x: number, y: number];

export interface PlaneOptions {
  /** Screen x of the plane's back corner (grid origin). */
  cx: number;
  /** Screen y of the plane's back corner. */
  oy: number;
  size?: number;
  n?: number;
  pattern?: Pattern;
}

/** One plane ready to draw: its outline and one path per brightness level. */
export interface Plane {
  outline: string;
  dots: { level: Exclude<Level, 0>; d: string }[];
  /** The right-hand corner, where a ruler line attaches. */
  corner: Point;
}

const round = (v: number) => Math.round(v * 10) / 10;

/** Projects plane coordinates to screen coordinates. */
const project =
  (cx: number, oy: number) =>
  (x: number, y: number): Point => [
    round(cx + (x - y) * 0.866),
    round(oy + (x + y) * 0.5),
  ];

/** Builds a plane's outline and dot paths. */
export function plane({
  cx,
  oy,
  size = 190,
  n = 12,
  pattern = () => 2,
}: PlaneOptions): Plane {
  const p = project(cx, oy);
  const outline = [p(0, 0), p(size, 0), p(size, size), p(0, size)]
    .map((pt) => pt.join(","))
    .join(" ");
  const paths: Record<Exclude<Level, 0>, string[]> = { 1: [], 2: [], 3: [] };
  const step = size / n;
  for (let i = 0; i < n; i++) {
    for (let j = 0; j < n; j++) {
      const level = pattern(i, j);
      if (level === 0) continue;
      const [x, y] = p((i + 0.5) * step, (j + 0.5) * step);
      const r = level === 3 ? 3.6 : 2.2;
      const h = round(r * 0.58);
      paths[level].push(
        `M${x} ${round(y - h)}L${round(x + r)} ${y}L${x} ${round(y + h)}L${round(x - r)} ${y}Z`,
      );
    }
  }
  const dots = ([1, 2, 3] as const)
    .map((level) => ({ level, d: paths[level].join("") }))
    .filter((dot) => dot.d !== "");
  return { outline, dots, corner: p(size, 0) };
}

/** Opacity for each dot level. */
export const levelOpacity: Record<Exclude<Level, 0>, number> = {
  1: 0.3,
  2: 0.6,
  3: 1,
};
