import { readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { extname, join } from "node:path";
import { gzipSync } from "node:zlib";
import type { Plugin } from "vite";

// Text files worth compressing; fonts and images are compressed already.
const compressible = new Set([
  ".html",
  ".js",
  ".mjs",
  ".css",
  ".json",
  ".svg",
  ".txt",
  ".map",
  ".webmanifest",
  ".xml",
]);

/**
 * Replaces each text file in the build output with its gzipped copy
 * (name.gz) when that saves at least a tenth, so the Go binary embeds the
 * UI compressed and serves those bytes as they are (internal/httpkit,
 * UIHandler). Saves about 1.5 MB in bin/dock. index.html stays as it is:
 * it is small, and scripts check for it. gzipSync writes no time into the
 * header, so builds stay reproducible.
 */
export function gzipDist(): Plugin {
  let outDir = "";
  return {
    name: "dock-gzip-dist",
    apply: "build",
    configResolved(config) {
      outDir = config.build.outDir;
    },
    closeBundle() {
      const walk = (dir: string) => {
        for (const entry of readdirSync(dir, { withFileTypes: true })) {
          const path = join(dir, entry.name);
          if (entry.isDirectory()) {
            walk(path);
            continue;
          }
          if (
            entry.name === "index.html" ||
            !compressible.has(extname(entry.name))
          )
            continue;
          const raw = readFileSync(path);
          if (raw.length < 512) continue;
          const gz = gzipSync(raw, { level: 9 });
          if (gz.length >= (raw.length * 9) / 10) continue;
          writeFileSync(`${path}.gz`, gz);
          rmSync(path);
        }
      };
      walk(outDir);
    },
  };
}
