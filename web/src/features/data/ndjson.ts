import type { DataQueryFrame } from "./types";

/**
 * Splits the NDJSON body of POST /data/query into frames as it arrives.
 * Both hosts stream the same framing (ADR 0016 section 8). The caller's
 * abort signal closes the request; the server then cancels the query.
 */
export async function* frames(
  stream: ReadableStream<Uint8Array<ArrayBuffer>>,
): AsyncGenerator<DataQueryFrame> {
  const reader = stream.pipeThrough(new TextDecoderStream()).getReader();
  let buffered = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffered += value;
      let newline = buffered.indexOf("\n");
      while (newline >= 0) {
        const line = buffered.slice(0, newline).trim();
        buffered = buffered.slice(newline + 1);
        if (line) yield JSON.parse(line) as DataQueryFrame;
        newline = buffered.indexOf("\n");
      }
    }
    if (buffered.trim()) yield JSON.parse(buffered) as DataQueryFrame;
  } finally {
    reader.releaseLock();
  }
}
