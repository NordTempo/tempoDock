/** How a log line reads: an error, a warning, or neither. */
export type LogLevel = "error" | "warn" | null;

// A level the line states itself: JSON ("level":"error", "lvl", "severity",
// pino's numeric "level":50), logfmt (level=error) or a tab-separated
// column (Caddy's and zap's console format). The first one wins.
const stated =
  /"(?:level|lvl|severity)"\s*:\s*"?([a-z]+|\d+)|(?:^|\s)(?:level|lvl|severity)="?([a-z]+)|\t(DEBUG|INFO|WARN|ERROR|FATAL|PANIC)\t/i;

// Redis's (and Valkey's) own format: "1:M 13 Nov 2026 11:25:50.521 * msg",
// "." debug, "-" verbose, "*" notice, "#" warning (its errors too).
const redis =
  /^\d+:[XCSM] \d\d [A-Za-z]{3} \d{4} \d\d:\d\d:\d\d\.\d{3} ([.\-*#]) /;

// Error words, or an HTTP 5xx status between spaces ("POST /api 502 31ms").
const errorWords = /\b(error|fatal|panic|exception)\b|\s5\d\d\s/i;

const levels = new Map<string, LogLevel>(
  Object.entries({
    error: "error",
    err: "error",
    fatal: "error",
    panic: "error",
    crit: "error",
    critical: "error",
    alert: "error",
    emerg: "error",
    emergency: "error",
    warn: "warn",
    warning: "warn",
    info: null,
    notice: null,
    debug: null,
    trace: null,
  } satisfies Record<string, LogLevel>),
);

/**
 * How a level word reads (lower case: "warning" is a warning, "notice"
 * neither); undefined for a word it doesn't know. The logs table judges
 * a JSON or logfmt line by the level parseLog found with it.
 */
export function namedLevel(word: string): LogLevel | undefined {
  return levels.get(word);
}

/**
 * Judges a log line by the level it states, so a server that logs
 * everything to stderr (Caddy) or says "error" in an info message isn't
 * read as failing. Go's errparse.LineLevel judges the Errors tab's lines
 * the same way. A line without a known level falls back to error
 * words. The stream (stdout or stderr) plays no part. Used by the logs
 * table's level column and row rule, its level switch and the chart.
 */
export function logLevel(text: string): LogLevel {
  const r = redis.exec(text);
  if (r) {
    if (r[1] !== "#") return null;
    return errorWords.test(text) ? "error" : "warn";
  }
  const m = stated.exec(text);
  const word = (m?.[1] ?? m?.[2] ?? m?.[3])?.toLowerCase();
  if (word !== undefined) {
    // pino and bunyan: 40 warn, 50 error, 60 fatal.
    if (/^\d+$/.test(word)) {
      const n = Number(word);
      return n >= 50 ? "error" : n >= 40 ? "warn" : null;
    }
    const level = namedLevel(word);
    if (level !== undefined) return level;
  }
  return errorWords.test(text) ? "error" : null;
}
