/** A log line split for the logs table's columns. */
export type ParsedLog = {
  /** The level the line states (info, warn, ...), lower case; null if none. */
  level: string | null;
  /** The message; the whole line for plain text. */
  msg: string;
  /** Every other key and value, in the line's order; none for plain text. */
  fields: [key: string, value: string][];
};

const levelKeys = new Set(["level", "lvl", "severity"]);
const msgKeys = new Set(["msg", "message"]);

// pino's and bunyan's numeric levels.
const numbered = new Map(
  Object.entries({
    "10": "trace",
    "20": "debug",
    "30": "info",
    "40": "warn",
    "50": "error",
    "60": "fatal",
  }),
);

/**
 * Splits a JSON or logfmt line into its level, message and fields, for
 * the logs table. Anything else (or a line that only looks like it) is
 * plain text: the whole line is the message.
 */
export function parseLog(text: string): ParsedLog {
  const pairs = text.startsWith("{") ? jsonPairs(text) : logfmtPairs(text);
  if (!pairs) return { level: null, msg: text, fields: [] };
  let level: string | null = null;
  let msg: string | null = null;
  const fields: ParsedLog["fields"] = [];
  for (const [key, value] of pairs) {
    if (level === null && levelKeys.has(key)) {
      level = numbered.get(value) ?? value.toLowerCase();
    } else if (msg === null && msgKeys.has(key)) {
      msg = value;
    } else {
      fields.push([key, value]);
    }
  }
  return { level, msg: msg ?? "", fields };
}

/** A JSON object's keys and values, nested values as JSON; null if not one. */
function jsonPairs(text: string): ParsedLog["fields"] | null {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  return Object.entries(value).map(([k, v]) => [
    k,
    typeof v === "string" ? v : JSON.stringify(v),
  ]);
}

const logfmtKey = /^[\w.@/-]+$/;

/**
 * logfmt's pairs (key=value, key="quoted \"value\""), or null when any
 * word isn't one or there are fewer than two: "GET /api 200" and
 * "ready port=80" stay plain text.
 */
function logfmtPairs(text: string): ParsedLog["fields"] | null {
  const pairs: ParsedLog["fields"] = [];
  let i = 0;
  while (i < text.length) {
    if (text[i] === " ") {
      i++;
      continue;
    }
    const eq = text.indexOf("=", i);
    const space = text.indexOf(" ", i);
    if (eq < 0 || (space >= 0 && space < eq)) return null;
    const key = text.slice(i, eq);
    if (!logfmtKey.test(key)) return null;
    i = eq + 1;
    let value = "";
    if (text[i] === '"') {
      i++;
      while (i < text.length && text[i] !== '"') {
        if (text[i] === "\\" && i + 1 < text.length) i++;
        value += text[i];
        i++;
      }
      if (i >= text.length) return null;
      i++;
    } else {
      const end = text.indexOf(" ", i);
      value = text.slice(i, end < 0 ? text.length : end);
      i = end < 0 ? text.length : end;
    }
    pairs.push([key, value]);
  }
  return pairs.length >= 2 ? pairs : null;
}
