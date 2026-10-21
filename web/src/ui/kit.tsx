import type { ComponentProps, ReactNode } from "react";
import type { Tone } from "./status";

// Tailwind needs whole class names, so tones map to literal classes.
export const toneText: Record<Tone, string> = {
  ok: "text-ok",
  bad: "text-bad",
  warn: "text-warn",
  run: "text-run",
  off: "text-off",
};

const toneBg: Record<Tone, string> = {
  ok: "bg-ok",
  bad: "bg-bad",
  warn: "bg-warn",
  run: "bg-run",
  off: "bg-off",
};

/** Static status square. Hollow means "not started". */
export function Glyph({ tone, hollow }: { tone: Tone; hollow?: boolean }) {
  return (
    <i
      aria-hidden
      className={`mr-[7px] inline-block size-2 shrink-0 ${
        hollow ? "border border-off" : toneBg[tone]
      }`}
    />
  );
}

/** Glyph plus label, the label colored only when it needs attention. */
export function Status({
  tone,
  label,
  colorLabel = tone === "bad" || tone === "warn",
}: {
  tone: Tone;
  label: string;
  colorLabel?: boolean;
}) {
  return (
    <span className="inline-flex items-baseline">
      <Glyph tone={tone} />
      <span className={colorLabel ? toneText[tone] : undefined}>{label}</span>
    </span>
  );
}

export function Button({
  primary,
  className = "",
  ...props
}: ComponentProps<"button"> & { primary?: boolean }) {
  return (
    <button
      type="button"
      className={`border px-2.5 py-[3px] disabled:border-line2! disabled:text-faint ${
        primary ? "border-white" : "border-line2 hover:border-[#444]"
      } ${className}`}
      {...props}
    />
  );
}

/** Text-only action, dim until hovered. */
export function Action({ className = "", ...props }: ComponentProps<"button">) {
  return (
    <button
      type="button"
      className={`text-dim hover:text-white disabled:text-faint ${className}`}
      {...props}
    />
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="border border-line2 px-1 font-mono text-2xs text-dim">
      {children}
    </kbd>
  );
}

/** Section heading with an optional dim note and right-aligned actions. */
export function Heading({
  children,
  note,
  actions,
  id,
}: {
  children: ReactNode;
  note?: ReactNode;
  actions?: ReactNode;
  id?: string;
}) {
  return (
    <h2
      id={id}
      className="flex items-baseline gap-3 pt-[18px] pb-1.5 font-semibold"
    >
      {children}
      {note && <span className="font-normal text-dim">{note}</span>}
      {actions && (
        <span className="ml-auto flex gap-4 font-normal">{actions}</span>
      )}
    </h2>
  );
}

/** Usage bar for memory and disk; turns yellow above 80%. */
export function Bar({ fraction }: { fraction: number }) {
  const clamped = Math.min(1, Math.max(0, fraction));
  return (
    <span className="relative ml-1.5 inline-block h-[3px] w-[46px] bg-[#222] align-middle">
      <i
        className={`absolute inset-y-0 left-0 ${clamped >= 0.8 ? "bg-warn" : "bg-[#bbb]"}`}
        style={{ width: `${clamped * 100}%` }}
      />
    </span>
  );
}

export function ErrorText({ error }: { error: Error | null }) {
  if (!error) return null;
  return <span className="text-xs text-bad">{error.message}</span>;
}

export const inputClass =
  "border-b border-line2 bg-black px-0 py-0.5 outline-none focus:border-white placeholder:text-faint";
