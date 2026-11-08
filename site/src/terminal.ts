/** Colours a terminal line can take, as Tailwind text classes. */
export const tones = {
  cmd: "text-white",
  meta: "text-meta",
  ok: "text-deploys",
  warn: "text-domains",
  bad: "text-errors",
  info: "text-analytics",
} as const;

export type Tone = keyof typeof tones;

/** One line of a terminal transcript. */
export interface Line {
  text: string;
  tone: Tone;
}
