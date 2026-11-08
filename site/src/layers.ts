import type { Pattern } from "./iso";

/** One layer of the feature stack in the hero, bottom to top. */
export interface Layer {
  id: string;
  name: string;
  detail: string;
  color: string;
  pattern: Pattern;
}

const prompt = new Set([
  "3,3",
  "4,4",
  "5,5",
  "4,6",
  "3,7",
  "7,7",
  "8,7",
  "9,7",
]);
const bars = [2, 3, 3, 5, 4, 6, 5, 7, 8, 7, 9, 10];

export const layers: Layer[] = [
  {
    id: "server",
    name: "YOUR SERVER",
    detail: "29 MB idle, 0% CPU",
    color: "#8a8a8a",
    pattern: () => 2,
  },
  {
    id: "deploys",
    name: "DEPLOYS",
    detail: "releases, previews",
    color: "#4ade80",
    // Release rows, with the current one lit.
    pattern: (i, j) => (j === 4 && i < 9 ? 3 : j % 3 === 1 ? 2 : 0),
  },
  {
    id: "domains",
    name: "DOMAINS",
    detail: "automatic TLS, DNS",
    color: "#fbbf24",
    // Rings around a lit centre.
    pattern: (i, j) => {
      const d = Math.abs(i - 5.5) + Math.abs(j - 5.5);
      if (d <= 1) return 3;
      return (d >= 4 && d <= 5) || (d >= 8 && d <= 9) ? 2 : 0;
    },
  },
  {
    id: "errors",
    name: "ERRORS",
    detail: "frontend, logs, 5xx",
    color: "#f87171",
    // Scattered noise and one cluster.
    pattern: (i, j) =>
      Math.hypot(i - 8, j - 3) < 1.8 ? 3 : (i * 7 + j * 13) % 9 === 0 ? 2 : 0,
  },
  {
    id: "analytics",
    name: "ANALYTICS",
    detail: "visits, vitals, trends",
    color: "#67e8f9",
    // A rising bar chart.
    pattern: (i, j) => {
      const top = 11 - (bars[i] ?? 0);
      if (j === top) return 3;
      return j > top && i % 2 === 0 ? 2 : 0;
    },
  },
  {
    id: "agents",
    name: "AGENTS",
    detail: "CLI, MCP, scoped tokens",
    color: "#a78bfa",
    // A ">_" prompt.
    pattern: (i, j) =>
      prompt.has(`${i},${j}`) ? 3 : (i + j) % 2 === 0 ? 1 : 0,
  },
];
