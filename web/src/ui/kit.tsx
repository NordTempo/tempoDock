import {
  type ComponentProps,
  type ReactNode,
  useEffect,
  useRef,
  useState,
} from "react";
import type { AuditEvent, EnvironmentLock } from "../data/types";
import { CliHint } from "./CliHint";
import { clock } from "./format";
import { Icon } from "./icons";
import { type GlyphChar, type State, toneText } from "./status";

/**
 * A status glyph. ● ○ ◐ are CSS dots (filled, ring, open ring; never
 * animated), everything else text.
 */
export function Glyph({ char }: { char: GlyphChar }) {
  if (char === "●") return <span className="dot" aria-hidden />;
  if (char === "○") return <span className="ring" aria-hidden />;
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
      return <span className="text-info">◇ {name}</span>;
    case "user":
      return <span>○ {name}</span>;
    case "system":
      return <span className="text-mut">{name}</span>;
  }
}

/**
 * Whether a lock holds now: an environment opened for a while counts as
 * unlocked until then and locks again by itself (ADR 0044). Read when
 * rendering, so no timer is needed.
 */
export const lockHolds = (lock: EnvironmentLock, now = Date.now()) =>
  !lock.open_until || Date.parse(lock.open_until) <= now;

/**
 * A padlock after a locked environment's name; faint while it is open for a
 * while. Nothing for an unlocked one.
 */
export function LockMark({ lock }: { lock: EnvironmentLock | undefined }) {
  if (!lock) return null;
  const holds = lockHolds(lock);
  return (
    <span
      className={`inline-flex align-[-1px] ${holds ? "text-warn-text" : "text-faint"}`}
      title={
        holds
          ? "locked"
          : `open until ${clock(lock.open_until ?? "")}, then locked`
      }
    >
      <Icon name="lock" size={11} />
      <span className="sr-only">{holds ? " locked" : " open"}</span>
    </span>
  );
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

/**
 * An action button with its `dock` CLI equivalent shown on hover; a plain
 * button where there is no CLI (tempoBase).
 */
export function Act({ cli, ...props }: ButtonProps & { cli?: string }) {
  if (cli === undefined) return <Button {...props} />;
  return (
    <CliHint cli={cli} className="act">
      <Button {...props} />
    </CliHint>
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

/** A labeled checkbox for an on/off setting. */
export function Check({
  label,
  checked,
  onChange,
  disabled,
  title,
}: {
  label: ReactNode;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  title?: string;
}) {
  return (
    <label
      className={`inline-flex items-center gap-1.5 ${disabled ? "text-faint" : "cursor-pointer"}`}
      title={title}
    >
      <input
        type="checkbox"
        className="accent-fg"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label}
    </label>
  );
}

/**
 * Asks before a change that is hard to take back: what happens, the
 * button that does it (with its CLI equivalent) and Cancel.
 */
export function Confirm({
  children,
  action,
  cli,
  danger,
  pending,
  error,
  onConfirm,
  onCancel,
}: {
  children: ReactNode;
  action: string;
  cli?: string;
  danger?: boolean;
  pending?: boolean;
  error?: Error | null;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div
      role="alertdialog"
      aria-label={action}
      className={`flex flex-wrap items-center gap-x-3 gap-y-2 rounded-panel border bg-panel px-3.5 py-3 whitespace-normal ${danger ? "border-fail-edge" : "border-edge"}`}
    >
      <span className="min-w-0 flex-[1_1_320px]">{children}</span>
      <span className="flex items-center gap-2">
        <Button
          small
          danger={danger}
          primary={!danger}
          disabled={pending}
          onClick={onConfirm}
        >
          {action}
        </Button>
        <Button small onClick={onCancel}>
          Cancel
        </Button>
      </span>
      {cli && <code className="font-mono text-2xs text-faint">{cli}</code>}
      <ErrorText error={error} />
    </div>
  );
}

export function ErrorText({ error }: { error: Error | null | undefined }) {
  if (!error) return null;
  return <span className="text-fail">{error.message}</span>;
}

/**
 * A page's content column under its PageBar: 24px 28px padding, left
 * aligned, at most 1100px wide unless `width` says otherwise.
 */
export function Page({
  width = "max-w-[1100px]",
  gap = "gap-7",
  children,
}: {
  width?: string;
  gap?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={`flex w-full flex-1 flex-col px-4 pt-6 pb-10 sm:px-7 ${width} ${gap}`}
    >
      {children}
    </div>
  );
}

/**
 * The bar at the top of a page: an optional parent crumb ("blog /"), the
 * title, what follows it (an environment switch, a status) and actions on
 * the right. It sticks to the top while the page scrolls.
 */
export function PageBar({
  parent,
  title,
  after,
  actions,
}: {
  parent?: ReactNode;
  title: ReactNode;
  after?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="sticky top-0 z-10 flex min-h-[52px] flex-wrap items-center gap-x-2.5 gap-y-2 border-b border-line bg-page px-5 py-2.5">
      <nav aria-label="Breadcrumb" className="flex items-center gap-1.5">
        {parent && (
          <>
            <span className="text-mut">{parent}</span>
            <span aria-hidden className="text-faint">
              /
            </span>
          </>
        )}
        <h1 className="text-md font-semibold">{title}</h1>
      </nav>
      {after}
      {actions && (
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {actions}
        </div>
      )}
    </header>
  );
}

/** Page title with a line of context under it and actions on the right. */
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
    <div className="flex flex-wrap items-start gap-x-4 gap-y-2">
      <div className="flex min-w-0 flex-col gap-0.5">
        <h1 className="text-xl font-semibold">{title}</h1>
        {context && <p className="text-xs text-mut">{context}</p>}
      </div>
      {actions && (
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {actions}
        </div>
      )}
    </div>
  );
}

/** Section heading with a faint aside and optional right-aligned links. */
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
    <div className="mb-2.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
      <h2 id={id} className="text-sm font-semibold">
        {children}
      </h2>
      {aside && <span className="text-xs text-faint">{aside}</span>}
      {right && (
        <span className="ml-auto flex gap-4 text-xs text-mut">{right}</span>
      )}
    </div>
  );
}

/**
 * One term and its value in a <dl>: the release page's facts, the CLI
 * login and access pages. The <dl> sets the grid (and `.props` the lines).
 */
export function Prop({
  label,
  children,
}: {
  label: ReactNode;
  children: ReactNode;
}) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

/** A table that scrolls sideways instead of squeezing on narrow screens. */
export function Scroll({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>;
}

/** A bordered, rounded panel around a table or list; scrolls sideways. */
export function Panel({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`panel ${className}`}>
      <div className="overflow-x-auto">{children}</div>
    </div>
  );
}

/** A row of labelled numbers between two lines: the console's summaries. */
export function Facts({ children }: { children: ReactNode }) {
  return (
    <dl className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] border-y border-line">
      {children}
    </dl>
  );
}

/** One number in Facts, with an optional line under it. */
export function Fact({
  label,
  note,
  children,
}: {
  label: string;
  note?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-0.5 border-soft px-3 py-2.5 not-last:border-r">
      <dt className="text-2xs text-faint">{label}</dt>
      <dd className="text-md font-semibold">{children}</dd>
      {note && <dd className="text-2xs text-mut">{note}</dd>}
    </div>
  );
}

/**
 * A small popup menu under its button. It is fixed to the viewport, so a
 * panel's overflow can't clip it; scrolling, Escape or a click outside
 * closes it.
 */
export function Menu({
  label,
  button,
  right,
  className = "",
  children,
}: {
  label: string;
  button: ReactNode;
  /** Line up with the button's right edge, for menus at the end of a row. */
  right?: boolean;
  className?: string;
  children: (close: () => void) => ReactNode;
}) {
  const [at, setAt] = useState<DOMRect | null>(null);
  const root = useRef<HTMLSpanElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = () => setAt(null);
    const onMouse = (event: MouseEvent) => {
      const inside =
        event.target instanceof Node && root.current?.contains(event.target);
      if (!inside) close();
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      close();
      trigger.current?.focus();
    };
    document.addEventListener("mousedown", onMouse);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("mousedown", onMouse);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [at]);
  return (
    <span ref={root} className={`relative ${className}`}>
      <button
        ref={trigger}
        type="button"
        aria-label={label}
        aria-expanded={!!at}
        onClick={(e) =>
          setAt(at ? null : e.currentTarget.getBoundingClientRect())
        }
        className="flex w-full items-center rounded-ctl"
      >
        {button}
      </button>
      {at && (
        <div
          style={{
            top: at.bottom + 4,
            ...(right
              ? { right: document.documentElement.clientWidth - at.right }
              : { left: at.left }),
          }}
          className="fixed z-40 flex min-w-44 flex-col rounded-ctl border border-edge bg-panel p-1 shadow-lg shadow-black/40"
        >
          {children(() => setAt(null))}
        </div>
      )}
    </span>
  );
}

/** A row in a Menu. */
export const menuItem =
  "flex items-center gap-2 rounded-[4px] px-2.5 py-1.5 text-left text-sec no-underline hover:bg-sel hover:text-fg aria-[current=page]:font-medium aria-[current=page]:text-fg";
