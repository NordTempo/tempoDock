import {
  type ComponentProps,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import type { AuditEvent, EnvironmentLock } from "../data/types";
import { CliHint } from "./CliHint";
import { Dialog } from "./Dialog";
import { clock } from "./format";
import { Icon } from "./icons";
import { type GlyphChar, type State, toneText } from "./status";

/**
 * A status glyph. ● ○ ◐ ◌ are CSS dots (filled, ring, half-filled ring,
 * dashed ring; never animated), everything else text.
 */
export function Glyph({ char }: { char: GlyphChar }) {
  if (char === "●") return <span className="dot" aria-hidden />;
  if (char === "○") return <span className="ring" aria-hidden />;
  if (char === "◌") return <span className="ring nap" aria-hidden />;
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
    <span
      data-tone={state.tone}
      className={`state ${toneText[state.tone]} ${className}`}
    >
      {state.glyph && (
        <>
          <Glyph char={state.glyph} />{" "}
        </>
      )}
      {state.label}
    </span>
  );
}

/**
 * Who did something: ◇ for tokens (agents, CI), ○ for people. The token
 * `dock admin bootstrap` makes at install, named "bootstrap", reads as
 * "setup token".
 */
/**
 * How an actor's name reads in plain text: the install's first token
 * (`dock admin bootstrap` names it "bootstrap") is the setup token.
 * Actor uses it, and plain-text lines ("set by", "manual by") too.
 */
export const actorLabel = (name: string) =>
  name === "bootstrap" ? "setup token" : name;

export function Actor({
  name,
  kind,
}: {
  name: string;
  kind: AuditEvent["actor_kind"];
}) {
  switch (kind) {
    case "token":
      return (
        <span
          className="text-info"
          title={
            name === "bootstrap"
              ? "The token named bootstrap: made by dock admin bootstrap when Dock was installed"
              : undefined
          }
        >
          ◇ {actorLabel(name)}
        </span>
      );
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
          : `lock paused until ${clock(lock.open_until ?? "")}, then locked again`
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
  /**
   * Red, for destructive actions (docs/design/README.md): outlined on the
   * page's button that opens the question (Remove, Revoke, Delete),
   * filled in the Confirm that runs it. Undoable actions stay neutral.
   */
  danger?: boolean;
  /**
   * Why the button is disabled, shown as text beside it while it is (a
   * title alone is invisible to touch and keyboard users).
   */
  reason?: string;
};

/**
 * The kit's button. With `disabled` and a `reason`, the reason follows it
 * as faint text and describes it for screen readers.
 */
export function Button({
  primary,
  small,
  danger,
  reason,
  className = "",
  ...props
}: ButtonProps) {
  const id = useId();
  const why = props.disabled ? reason : undefined;
  const button = (
    <button
      type="button"
      aria-describedby={why ? id : undefined}
      className={`btn ${primary ? "pri" : ""} ${small ? "sm" : ""} ${danger ? "danger" : ""} ${className}`}
      {...props}
    />
  );
  if (!why) return button;
  return (
    <>
      {button}
      <span id={id} className="self-center px-2 text-xs text-faint">
        {why}
      </span>
    </>
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

/**
 * "Copy command" for a dialog's `dock` equivalent: the command is its
 * tooltip and goes to the clipboard on click. A small bordered button in
 * every dialog, whether it renders inside the page's main or not.
 */
function CopyCommand({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="btn sm"
      title={text}
      aria-label={`Copy command: ${text}`}
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? "Copied" : "Copy command"}
    </button>
  );
}

/** A labeled checkbox for an on/off setting. */
export function Check({
  label,
  checked,
  onChange,
  disabled,
  reason,
  title,
}: {
  label: ReactNode;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  /** Why it is disabled, shown on a line under it while it is. */
  reason?: string;
  title?: string;
}) {
  const id = useId();
  const why = disabled ? reason : undefined;
  return (
    <label
      className={`inline-flex flex-wrap items-center gap-x-1.5 ${disabled ? "text-faint" : "cursor-pointer"}`}
      title={title}
    >
      <input
        type="checkbox"
        // A 16px box with margin: a 24px target (WCAG 2.5.8).
        className="m-[4px] size-[16px] accent-fg"
        checked={checked}
        disabled={disabled}
        aria-describedby={why ? id : undefined}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label}
      {/* A muted second line under the box, wrapping in narrow cells. */}
      {why && (
        <span
          id={id}
          className="basis-full pl-6 text-xs whitespace-normal text-mut"
        >
          {why}
        </span>
      )}
    </label>
  );
}

/**
 * The app's one confirmation: a small modal that asks before a change that
 * is hard to take back. The title asks the question ("Roll back to r7?",
 * from `action` unless `title` is given), `children` say in plain words what
 * will happen, and the button names the action. Copy command in the
 * footer gives the CLI equivalent. `typed` asks the person to type a name first; keep it for
 * destructive changes that can't be undone (deleting an organization).
 * Render it only while asking: Esc, the backdrop and Cancel call onCancel.
 * The safe button has focus, so Enter never destroys by accident.
 */
export function Confirm({
  children,
  title,
  action,
  cli,
  danger,
  typed,
  pending,
  error,
  onConfirm,
  onCancel,
}: {
  children: ReactNode;
  title?: string;
  action: string;
  cli?: string;
  danger?: boolean;
  typed?: string;
  pending?: boolean;
  error?: Error | null;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState("");
  const ready = !pending && (!typed || text.trim() === typed);
  return (
    <Dialog title={title ?? `${action}?`} onClose={onCancel}>
      <form
        className="flex flex-col"
        onSubmit={(e) => {
          // A Confirm can open inside another form; its submit is its own.
          e.preventDefault();
          e.stopPropagation();
          if (ready) onConfirm();
        }}
      >
        <div className="flex flex-col gap-2">
          <div className="text-xs leading-relaxed text-sec [&_b]:font-semibold [&_b]:text-fg">
            {children}
          </div>
          {typed && (
            <label className="mt-1 flex flex-col gap-1.5 text-xs text-mut">
              <span>
                Type <b className="font-mono font-medium text-fg">{typed}</b> to
                confirm
              </span>
              <input
                className="field font-mono"
                value={text}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setText(e.target.value)}
              />
            </label>
          )}
          <ErrorText error={error} />
        </div>
        <DialogFoot cli={cli}>
          <Button data-autofocus={typed ? undefined : true} onClick={onCancel}>
            Cancel
          </Button>
          <Button
            type="submit"
            danger={danger}
            primary={!danger}
            className={danger ? "solid" : ""}
            disabled={!ready}
          >
            {action}
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}

/**
 * Every dialog's footer, edge to edge under a line: one row with Copy
 * command (the CLI equivalent) and `note` (why it can't go on yet, or the
 * error) on the left and the buttons, Cancel first and the named action
 * last, on the right. Put it last in a Dialog's body (it bleeds into the body's
 * padding). `className` sets the space above it: 16px by default, less
 * where the form's own gap already spaces it.
 */
export function DialogFoot({
  cli,
  note,
  className = "mt-4",
  children,
}: {
  cli?: string | readonly string[];
  note?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  const lines = typeof cli === "string" ? [cli] : (cli ?? []);
  return (
    <div
      className={`-mx-5 -mb-4 flex flex-col gap-2.5 border-t border-line px-5 py-3 ${className}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        {/* The CLI equivalent one click away, not a second line of text. */}
        {lines.length > 0 && <CopyCommand text={lines.join("\n")} />}
        <span className="min-w-0 flex-1 text-xs">{note}</span>
        {children}
      </div>
    </div>
  );
}

/** An error message in place, announced to screen readers when it appears. */
export function ErrorText({ error }: { error: Error | null | undefined }) {
  if (!error) return null;
  return (
    <span role="alert" className="text-fail">
      {error.message}
    </span>
  );
}

/**
 * A page's content column under its PageBar: 24px 28px padding, left
 * aligned, 24px between sections, at most 1400px wide. Every page of
 * tables uses that one width, so edges line up from page to page; a page
 * that is one form uses 760px.
 */
export function Page({
  width = "max-w-[1400px]",
  gap = "gap-6",
  children,
}: {
  width?: string;
  gap?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={`page flex w-full flex-1 flex-col px-4 pt-6 pb-10 sm:px-7 ${width} ${gap}`}
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
    <header className="page-bar sticky top-(--shell-top) z-10 flex min-h-[64px] flex-wrap items-center gap-x-2.5 gap-y-2 border-b border-line bg-page px-4 py-2.5 sm:px-7">
      {/* Two lines (chosen in the 2026-11 trial): the way up, small, over a
          large title with what follows it. */}
      <div className="flex min-w-0 flex-col">
        {/* The line is kept when there is no way up, so every title sits
            at the same height. */}
        {parent ? (
          // 18px like the spacer below, whatever its links' targets are,
          // so every title sits at the same height.
          <nav
            aria-label="Breadcrumb"
            className="flex h-[18px] items-center text-xs text-mut"
          >
            {parent}
          </nav>
        ) : (
          <span aria-hidden className="h-[18px]" />
        )}
        <div className="flex flex-wrap items-center gap-x-2.5">
          <h1 className="text-2xl leading-7 font-semibold tracking-[-0.01em]">
            {title}
          </h1>
          {after}
        </div>
      </div>
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
      <h2 id={id} className="text-lg font-semibold">
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
      <dt className="text-xs text-mut">{label}</dt>
      <dd className="text-lg font-semibold">{children}</dd>
      {note && <dd className="text-xs text-mut">{note}</dd>}
    </div>
  );
}

/**
 * A small popup menu under its button (role="menu"): give each row
 * `role="menuitem"` and className={menuItem}; labels inside get
 * role="presentation", dividers role="separator". Arrow keys move
 * between items. It is fixed to the viewport, so a
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
  const popup = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = () => setAt(null);
    const onMouse = (event: MouseEvent) => {
      const inside =
        event.target instanceof Node && root.current?.contains(event.target);
      if (!inside) close();
    };
    // Esc closes; the arrow keys move between the items (role="menuitem").
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        const items = [
          ...(popup.current?.querySelectorAll<HTMLElement>(
            '[role^="menuitem"]:not(:disabled)',
          ) ?? []),
        ];
        if (items.length === 0) return;
        event.preventDefault();
        const at = items.indexOf(document.activeElement as HTMLElement);
        const step = event.key === "ArrowDown" ? 1 : -1;
        items[(at + step + items.length) % items.length]?.focus();
        return;
      }
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
        aria-haspopup="menu"
        aria-expanded={!!at}
        onClick={(e) =>
          setAt(at ? null : e.currentTarget.getBoundingClientRect())
        }
        className="flex min-h-[24px] w-full min-w-[24px] items-center rounded-ctl"
      >
        {button}
      </button>
      {at && (
        <div
          ref={popup}
          role="menu"
          aria-label={label}
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
  "flex items-center gap-2 rounded-tag px-2.5 py-1.5 text-left text-sec no-underline hover:bg-sel hover:text-fg aria-[current=page]:font-medium aria-[current=page]:text-fg";
