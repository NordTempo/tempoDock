/**
 * Parses .env text: KEY=value lines, `export` prefixes, # comments, and
 * single- or double-quoted values (double quotes understand \n).
 */
export function parseDotenv(text: string) {
  const out: Array<{ name: string; value: string }> = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim().replace(/^export\s+/, "");
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const name = line.slice(0, eq).trim();
    let value = line.slice(eq + 1).trim();
    const quote = value[0];
    if (
      (quote === '"' || quote === "'") &&
      value.endsWith(quote) &&
      value.length > 1
    ) {
      value = value.slice(1, -1);
      if (quote === '"')
        value = value.replace(/\\n/g, "\n").replace(/\\"/g, '"');
    } else {
      value = value.replace(/\s+#.*$/, "");
    }
    out.push({ name, value });
  }
  return out;
}
