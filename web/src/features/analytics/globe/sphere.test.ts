// Open /tests/globe.html under Vite. No backend: pure functions only.
import { centres } from "./centres";
import { land } from "./land";
import { facing, landDots, turn, vec } from "./sphere";

function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}

const near = (a: number, b: number) => Math.abs(a - b) < 1e-9;

check(landDots(land).length === land.dots, "the land decodes to its dots");

// The globe opens facing where the visitors are.
const oslo = vec(59.9, 10.7);
const seen = turn(oslo, facing([{ at: oslo, weight: 1 }]));
check(
  near(seen[0], 0) && near(seen[1], 0) && near(seen[2], 1),
  "one country's visitors face the viewer",
);
const sydney = turn(vec(-33.9, 151.2), facing([{ at: oslo, weight: 1 }]));
check(sydney[2] < 0, "the other side of the world is behind the globe");

const [lat, lon] = centres.NO ?? [0, 0];
check(
  lat > 58 && lat < 71 && lon > 4 && lon < 31,
  "Norway's centre is in Norway",
);

document.title = "PASS: globe";
const results = document.getElementById("results");
if (results) results.textContent = "PASS: globe";
