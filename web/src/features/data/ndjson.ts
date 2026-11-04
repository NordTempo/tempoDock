import type { DataQueryFrame } from "./types";

/**
 * Splits an NDJSON body into parsed lines as it arrives: the data panel's
 * POST /data/query frames (both hosts stream the same framing, ADR 0016
 * section 8) by default, or any other line type T. Only newly read text
 * is searched for line ends, so a line spanning many reads (a replay
 * chunk) costs one pass. The caller's abort signal closes the request;
 * for a query the server then cancels it.
 */
export async function* frames<T = DataQueryFrame>(
  stream: ReadableStream<Uint8Array<ArrayBuffer>>,
): AsyncGenerator<T> {
  const reader = stream.pipeThrough(new TextDecoderStream()).getReader();
  let buffered = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      let start = 0;
      for (
        let newline = value.indexOf("\n");
        newline >= 0;
        newline = value.indexOf("\n", start)
      ) {
        const line = (buffered + value.slice(start, newline)).trim();
        buffered = "";
        start = newline + 1;
        if (line) yield JSON.parse(line) as T;
      }
      buffered += value.slice(start);
    }
    if (buffered.trim()) yield JSON.parse(buffered) as T;
  } finally {
    reader.releaseLock();
  }
}
