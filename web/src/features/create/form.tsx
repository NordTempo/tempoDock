import type { ReactNode } from "react";
import { Button, ErrorText } from "../../ui/kit";

// Shared parts of the create dialogs: a label column, name rules as core
// has them, and a footer with the CLI equivalent.

/** core's nameRe: DNS-safe, since service names are private hostnames. */
const nameRe = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;

/** core's reserved names: the UI's pages at /<org> and /<org>/<project>. */
const reserved = {
  org: [
    "account",
    "api",
    "assets",
    "cli-login",
    "console",
    "fonts",
    "invite",
    "login",
    "no-org",
  ],
  project: ["audit", "settings", "tokens"],
};

/**
 * What is wrong with a new name, said as what to do; undefined when it
 * is fine or still empty. `taken` holds the names already in use.
 */
export function nameProblem(
  kind: "org" | "project" | "environment" | "service",
  name: string,
  taken: readonly string[] = [],
) {
  if (!name) return undefined;
  if (!nameRe.test(name)) {
    return "Use 1 to 32 lowercase letters, digits or dashes, starting and ending with a letter or digit.";
  }
  if (kind === "service" && name === "shared") {
    return `"shared" is reserved for shared variables. Pick another name.`;
  }
  if ((kind === "org" || kind === "project") && reserved[kind].includes(name)) {
    return `${name} is a page in Dock. Pick another name.`;
  }
  if (taken.includes(name)) {
    const where = {
      org: "Dock",
      project: "organization",
      environment: "project",
      service: "environment",
    }[kind];
    return `${name} is taken in this ${where}. Pick another name.`;
  }
  return undefined;
}

/** Names are typed loosely: lower case, spaces and underscores as dashes. */
export const toName = (value: string) =>
  value.toLowerCase().replace(/[\s_]+/g, "-");

/**
 * Focuses an element in a dialog when it mounts. React's autoFocus runs
 * before <dialog> opens, where focus() does nothing; the autofocus
 * attribute is what showModal() looks for. Later (a result replacing a
 * form), focus() does it.
 */
export function autofocus(el: HTMLElement | null) {
  el?.setAttribute("autofocus", "");
  el?.focus();
}

/** The fields of a dialog: labels on the left, controls on the right. */
export function Fields({ children }: { children: ReactNode }) {
  return (
    <div className="grid grid-cols-[100px_minmax(0,1fr)] items-center gap-x-3 gap-y-2.5">
      {children}
    </div>
  );
}

/**
 * One labelled control in Fields; `note` goes under it, and `error` (what
 * to fix, once the person tried to submit or typed something wrong) in
 * its place, in red. The control marks itself with `invalid`.
 */
export function Field({
  label,
  children,
  note,
  error,
}: {
  label: string;
  children: ReactNode;
  note?: ReactNode;
  error?: string;
}) {
  return (
    <>
      {/* biome-ignore lint/a11y/noLabelWithoutControl: the control is a child */}
      <label className="contents">
        <span className={`text-xs ${error ? "text-fail" : "text-faint"}`}>
          {label}
        </span>
        {children}
      </label>
      {error ? (
        <span role="alert" className="col-start-2 -mt-1 text-xs text-fail">
          {error}
        </span>
      ) : (
        note && (
          <span className="col-start-2 -mt-1 text-xs text-mut">{note}</span>
        )
      )}
    </>
  );
}

/** A control's classes when its field has an error. */
export const invalid = (error: string | undefined) =>
  error ? "border-fail" : "";

/**
 * A label and controls that are not a single input (radios, checks); a
 * <label> around them would name them all.
 */
export function Group({
  label,
  children,
  note,
}: {
  label: string;
  children: ReactNode;
  note?: ReactNode;
}) {
  return (
    <>
      <span className="text-xs text-faint">{label}</span>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {children}
      </div>
      {note && (
        <span className="col-start-2 -mt-1 text-xs text-mut">{note}</span>
      )}
    </>
  );
}

/** A segmented radio group: one choice of a few, arrow keys move. */
export function Choice<T extends string>({
  label,
  name,
  value,
  options,
  onChange,
}: {
  label: string;
  name: string;
  value: T;
  options: readonly (readonly [T, string])[];
  onChange: (value: T) => void;
}) {
  return (
    <fieldset aria-label={label} className="seg">
      {options.map(([v, text]) => (
        <label
          key={v}
          className="cursor-pointer has-focus-visible:outline-2 has-focus-visible:-outline-offset-2 has-focus-visible:outline-info"
        >
          <input
            type="radio"
            name={name}
            value={v}
            checked={v === value}
            onChange={() => onChange(v)}
            className="sr-only"
          />
          {text}
        </label>
      ))}
    </fieldset>
  );
}

export const input = "field py-[5px] font-mono";

/**
 * The dialogs' one footer: the error or why it can't submit yet on the
 * left, Cancel and the named submit button on the right, and the CLI
 * commands that do the same under a line.
 */
export function Footer({
  submit,
  pending,
  disabled,
  reason = "Fill in the fields above first.",
  problem,
  error,
  cli,
  alt,
  onClose,
}: {
  submit: string;
  pending: boolean;
  disabled?: boolean;
  /** Shown while `disabled`, so a dimmed button never goes unexplained. */
  reason?: string;
  /**
   * What stopped a submit, in red ("Add a name and an image"), for forms
   * that keep the button enabled and mark the fields instead.
   */
  problem?: string;
  error: Error | null | undefined;
  cli: readonly string[];
  /** A second way to submit, before the named one ("Create only"). */
  alt?: ReactNode;
  onClose: () => void;
}) {
  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 text-xs">
          {error ? (
            <ErrorText error={error} />
          ) : problem ? (
            <span className="text-fail">{problem}</span>
          ) : (
            disabled && !pending && <span className="text-faint">{reason}</span>
          )}
        </span>
        <Button onClick={onClose}>Cancel</Button>
        {alt}
        <button
          type="submit"
          className="btn pri"
          disabled={pending || disabled}
        >
          {submit} <kbd>↵</kbd>
        </button>
      </div>
      <div className="mt-3 flex flex-col gap-0.5 border-t border-line pt-2.5 font-mono text-2xs break-all text-faint">
        {cli.map((line) => (
          <code key={line}>{line}</code>
        ))}
      </div>
    </>
  );
}
