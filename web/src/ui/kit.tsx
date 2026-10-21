import { type ComponentProps, type ReactNode, useState } from "react";
import type { AuditEvent } from "../data/types";
import { type GlyphChar, type State, toneText } from "./status";

/** A status glyph. ◐ is a static CSS half circle, everything else text. */
export function Glyph({ char }: { char: GlyphChar }) {
  if (char === "◐") return <span className="pi" aria-hidden />;
  return <span aria-hidden>{char}</span>;
}

/** Glyph and label in the state's color: "● running". */
export function StateText({
  state,
  className = "",
}: {
  state: State;
  className?: string;
}) {
  return (
    <span className={`${toneText[state.tone]} ${className}`}>
      {state.glyph && (
        <>
          <Glyph char={state.glyph} />{" "}
        </>
      )}
      {state.label}
    </span>
  );
}

/** Who did something: ◇ for tokens (agents, CI), ○ for people. */
export function Actor({
  name,
  kind,
}: {
  name: string;
  kind: AuditEvent["actor_kind"];
}) {
  switch (kind) {
    case "token":
      return <span className="font-mono text-prog">◇ {name}</span>;
    case "user":
      return <span>○ {name}</span>;
    case "system":
      return <span className="text-mut">{name}</span>;
  }
}

/** A release's author, which is a token or a user by name only. */
export const actorKind = (name: string): AuditEvent["actor_kind"] =>
  name.includes("@") ? "user" : name === "system" ? "system" : "token";

type ButtonProps = ComponentProps<"button"> & {
  primary?: boolean;
  small?: boolean;
  danger?: boolean;
};

export function Button({
  primary,
  small,
  danger,
  className = "",
  ...props
}: ButtonProps) {
  return (
    <button
      type="button"
      className={`btn ${primary ? "pri" : ""} ${small ? "sm" : ""} ${danger ? "danger" : ""} ${className}`}
      {...props}
    />
  );
}

/** An action button with its `dock` CLI equivalent shown on hover. */
export function Act({ cli, ...props }: ButtonProps & { cli: string }) {
  return (
    <span className="act">
      <Button {...props} />
      <span className="cli">{cli}</span>
    </span>
  );
}

/** Copies text to the clipboard and says so for a moment. */
export function CopyButton({
  text,
  label = "Copy",
  primary,
}: {
  text: string;
  label?: string;
  primary?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      small
      primary={primary}
      aria-label={`${label}: ${text}`}
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}

export function ErrorText({ error }: { error: Error | null | undefined }) {
  if (!error) return null;
  return <span className="text-fail">{error.message}</span>;
}

/** The centered content column every page but the overview uses. */
export function Page({
  width = "max-w-[1240px]",
  gap = "gap-[22px]",
  children,
}: {
  width?: string;
  gap?: string;
  children: ReactNode;
}) {
  return (
    <main className={`mx-auto flex w-full flex-1 flex-col p-5 ${width} ${gap}`}>
      {children}
    </main>
  );
}

/** Page title with its context next to it and actions on the right. */
export function TitleRow({
  title,
  context,
  actions,
}: {
  title: ReactNode;
  context?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <h1 className="text-xl font-semibold">{title}</h1>
      {context && <span className="text-mut">{context}</span>}
      {actions && (
        <div className="ml-auto flex flex-wrap items-start gap-2.5">
          {actions}
        </div>
      )}
    </div>
  );
}

/** Section heading with a muted aside and optional right-aligned links. */
export function SectionHead({
  id,
  children,
  aside,
  right,
}: {
  id?: string;
  children: ReactNode;
  aside?: ReactNode;
  right?: ReactNode;
}) {
  return (
    <div className="mb-1 flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5">
      <h2 id={id} className="text-md font-semibold">
        {children}
      </h2>
      {aside && <span className="text-mut">{aside}</span>}
      {right && <span className="ml-auto flex gap-4">{right}</span>}
    </div>
  );
}

/** A table that scrolls sideways instead of squeezing on narrow screens. */
export function Scroll({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>;
}
