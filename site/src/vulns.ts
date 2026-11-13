import type { Line } from "./terminal";

/** `dock vulns` on a live image: what it has and how to fix it, as the CLI prints it. */
export const vulnLines: Line[] = [
  { text: "$ dock vulns -s web", tone: "cmd" },
  { text: "image            shop-web:41", tone: "meta" },
  { text: "base             node:22.9-alpine", tone: "meta" },
  { text: "vulnerabilities  1 critical, 2 high, 9 more", tone: "bad" },
  { text: "Rebuild on a newer base image: 1 critical, 1 high", tone: "warn" },
  { text: "  libexpat 2.6.2 → 2.6.3  critical CVE-2024-45491", tone: "meta" },
  { text: "Upgrade 1 package: 1 high", tone: "warn" },
  {
    text: "  path-to-regexp 0.1.7 → 0.1.10  high CVE-2024-45296",
    tone: "meta",
  },
];
