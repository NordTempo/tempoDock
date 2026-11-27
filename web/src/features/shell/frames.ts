// The runframe framing (internal/runframe) of a shell's WebSocket (ADR
// 0108): one type byte, a big-endian uint32 payload length, the payload.
// Up: stdin ("i"), close-stdin ("c"), resize ("r": cols, rows as uint16).
// Down: stdout ("o"), stderr ("e"), then one exit ("x", JSON).

/** A frame's type byte. */
export const Frame = {
  stdin: 0x69,
  closeStdin: 0x63,
  resize: 0x72,
  stdout: 0x6f,
  stderr: 0x65,
  exit: 0x78,
} as const;

/** One frame of type t around payload. */
export function frame(t: number, payload: Uint8Array = new Uint8Array()) {
  const out = new Uint8Array(5 + payload.length);
  const view = new DataView(out.buffer);
  out[0] = t;
  view.setUint32(1, payload.length);
  out.set(payload, 5);
  return out;
}

/** A resize frame for a terminal of cols × rows. */
export function resizeFrame(cols: number, rows: number) {
  const p = new Uint8Array(4);
  const view = new DataView(p.buffer);
  view.setUint16(0, cols);
  view.setUint16(2, rows);
  return frame(Frame.resize, p);
}

/**
 * Reads frames from the socket's messages, which need not end at frame
 * boundaries: push each message, and every whole frame comes out in order.
 */
export class FrameReader {
  private buf = new Uint8Array();

  push(chunk: Uint8Array): { type: number; payload: Uint8Array }[] {
    const all = new Uint8Array(this.buf.length + chunk.length);
    all.set(this.buf);
    all.set(chunk, this.buf.length);
    const out: { type: number; payload: Uint8Array }[] = [];
    let at = 0;
    while (all.length - at >= 5) {
      const n = new DataView(all.buffer, at + 1, 4).getUint32(0);
      if (all.length - at - 5 < n) break;
      out.push({ type: all[at] ?? 0, payload: all.slice(at + 5, at + 5 + n) });
      at += 5 + n;
    }
    this.buf = all.slice(at);
    return out;
  }
}
