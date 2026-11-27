import assert from "node:assert/strict";
import { test } from "node:test";
import { Frame, FrameReader, frame, resizeFrame } from "./frames.ts";

test("frames round-trip across message boundaries", () => {
  const hi = new TextEncoder().encode("hi");
  const both = new Uint8Array([
    ...frame(Frame.stdout, hi),
    ...frame(Frame.exit, new TextEncoder().encode("{}")),
  ]);
  const r = new FrameReader();
  assert.deepEqual(r.push(both.slice(0, 3)), []);
  const got = [...r.push(both.slice(3, 9)), ...r.push(both.slice(9))];
  assert.deepEqual(
    got.map((f) => [f.type, new TextDecoder().decode(f.payload)]),
    [
      [Frame.stdout, "hi"],
      [Frame.exit, "{}"],
    ],
  );
});

test("a resize carries cols then rows, big-endian", () => {
  assert.deepEqual(
    [...resizeFrame(300, 40)],
    [Frame.resize, 0, 0, 0, 4, 1, 44, 0, 40],
  );
});
