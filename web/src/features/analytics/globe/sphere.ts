import type { land as Land } from "./land";

// The globe's arithmetic, apart from drawing so a fixture can check it
// (tests/globe.html): points on the unit sphere and an orthographic view
// turned by yaw (about the axis through the poles) and pitch.

/** A point on the unit sphere: x east at 0° longitude, y north, z out of the screen. */
export type Vec = readonly [number, number, number];

const rad = Math.PI / 180;

/** The point at a latitude and longitude, in degrees. */
export const vec = (lat: number, lon: number): Vec => [
  Math.cos(lat * rad) * Math.sin(lon * rad),
  Math.sin(lat * rad),
  Math.cos(lat * rad) * Math.cos(lon * rad),
];

/** The land's dots, decoded from land.ts's rows of run lengths. */
export function landDots(land: typeof Land): Vec[] {
  const dots: Vec[] = [];
  land.rows.split(",").forEach((row, i) => {
    const lat = 90 - land.step / 2 - i * land.step;
    const runs = row.split(".").map((r) => Number.parseInt(r, 36));
    const n = runs.reduce((a, b) => a + b, 0);
    let k = 0;
    runs.forEach((len, j) => {
      // Runs alternate water and land, water first.
      if (j % 2) {
        for (let q = 0; q < len; q++) {
          dots.push(vec(lat, -180 + ((k + q + 0.5) * 360) / n));
        }
      }
      k += len;
    });
  });
  return dots;
}

/** Which way the globe faces: turned by yaw, then tipped by pitch (radians). */
export type View = { yaw: number; pitch: number };

/** A point as seen: x right, y up, z towards you (behind the globe below 0). */
export function turn(v: Vec, { yaw, pitch }: View): Vec {
  const x = v[0] * Math.cos(yaw) + v[2] * Math.sin(yaw);
  const z = -v[0] * Math.sin(yaw) + v[2] * Math.cos(yaw);
  return [
    x,
    v[1] * Math.cos(pitch) - z * Math.sin(pitch),
    v[1] * Math.sin(pitch) + z * Math.cos(pitch),
  ];
}

/** The view that faces the weighted middle of the points (where most visitors are). */
export function facing(points: { at: Vec; weight: number }[]): View {
  let x = 0;
  let y = 0;
  let z = 0;
  for (const { at, weight } of points) {
    x += at[0] * weight;
    y += at[1] * weight;
    z += at[2] * weight;
  }
  if (x === 0 && y === 0 && z === 0) return { yaw: 0, pitch: 0.5 };
  return { yaw: -Math.atan2(x, z), pitch: Math.atan2(y, Math.hypot(x, z)) };
}

/** Pitch stays short of the poles, so north stays up. */
export const clampPitch = (p: number) => Math.max(-1.2, Math.min(1.2, p));
