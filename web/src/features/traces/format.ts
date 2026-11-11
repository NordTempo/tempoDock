// How traces show durations, as the CLI does: "0.4 ms", "12 ms",
// "1,284 ms", and seconds from 10 s.

/** A duration in milliseconds. */
export function ms(n: number) {
  if (n < 10) return `${n.toFixed(1)} ms`;
  if (n < 10_000) return `${Math.round(n).toLocaleString("en-US")} ms`;
  return `${(n / 1000).toFixed(1)} s`;
}
