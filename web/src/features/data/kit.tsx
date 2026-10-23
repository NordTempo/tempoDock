import { type ComponentProps, type ReactNode, useState } from "react";
import { CliHint } from "../../ui/CliHint";
import "./g.css";

// Style G's small parts (./g.css), for the data panel in Dock and for
// tempoBase, which shares nothing else of Dock's UI kit.

type BtnProps = ComponentProps<"button"> & {
  primary?: boolean;
  small?: boolean;
  ghost?: boolean;
  danger?: boolean;
};

export function Btn({
  primary,
  small,
  ghost,
  danger,
  className = "",
  ...props
}: BtnProps) {
  const variant = [
    primary && "pri",
    small && "sm",
    ghost && "ghost",
    danger && "danger",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button
      type="button"
      className={`g-btn ${variant} ${className}`}
      {...props}
    />
  );
}

/**
 * An action button with its `dock` CLI equivalent shown on hover; a plain
 * button where the host has no CLI (tempoBase).
 */
export function Act({ cli, ...props }: BtnProps & { cli?: string }) {
  if (cli === undefined) return <Btn {...props} />;
  return (
    <CliHint cli={cli} className="g-act">
      <Btn {...props} />
    </CliHint>
  );
}

/** A key hint: "⌘↵", "E". */
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="g-kbd">{children}</kbd>;
}

/** Copies text to the clipboard and says so until the pointer leaves. */
export function CopyBtn({
  text,
  label = "Copy",
}: {
  text: string;
  label?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Btn
      small
      aria-label={`${label}: ${text}`}
      onMouseLeave={() => setCopied(false)}
      onBlur={() => setCopied(false)}
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setCopied(true);
      }}
    >
      {copied ? "Copied" : label}
    </Btn>
  );
}

export function ErrorText({ error }: { error: Error | null | undefined }) {
  if (!error) return null;
  return <span className="text-(--g-fail)">{error.message}</span>;
}

/** A table that scrolls sideways instead of squeezing on narrow screens. */
export function Scroll({ children }: { children: ReactNode }) {
  return <div className="min-w-0 overflow-x-auto">{children}</div>;
}

/** Who did something: a person by name, an agent (token) with a square mark. */
export function Who({
  name,
  kind,
}: {
  name: string;
  kind: "user" | "token" | "system";
}) {
  if (kind === "system") return <span className="text-(--g-mut)">{name}</span>;
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className={`g-avatar ${kind === "token" ? "agent" : ""}`}
        aria-hidden
      >
        {initials(name)}
      </span>
      <span className="font-medium">{name}</span>
    </span>
  );
}

/** "Ingrid Berg" → "IB", "ci-bot" → "C", "ada@x.no" → "A". */
export function initials(name: string) {
  const words =
    name
      .split("@")[0]
      ?.split(/[\s._-]+/)
      .filter(Boolean) ?? [];
  return (
    (words[0]?.[0] ?? "?") + (words.length > 1 ? (words[1]?.[0] ?? "") : "")
  ).toUpperCase();
}

/** The page or pane header: crumb, title, what's next to it, actions. */
export function Head({
  crumb,
  title,
  children,
  actions,
}: {
  crumb?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="g-head">
      {crumb && <span className="text-(--g-mut)">{crumb} /</span>}
      <h1>{title}</h1>
      {children}
      {actions && (
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {actions}
        </div>
      )}
    </header>
  );
}
