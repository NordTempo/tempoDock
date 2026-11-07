import { type ReactNode, useEffect, useRef, useState } from "react";
import { Button, DialogFoot, ErrorText } from "../../ui/kit";

// Shared parts of forms: a label column, name rules as core has them, the
// one validation pattern, a footer with the CLI equivalent, and the
// "Saved" mark for settings that save on change.

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
  kind: "org" | "project" | "environment" | "service" | "server",
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
    if (kind === "server") return `Another server is named ${name}.`;
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

/** "a name", "a name and an image", "a, b and c". */
const list = (items: readonly string[]) =>
  items.length < 2
    ? (items[0] ?? "")
    : `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`;

/**
 * The one validation pattern for every form: the submit button stays
 * enabled, and a submit with something missing or wrong marks the fields
 * instead (a red outline and a message under each, through Field's
 * `error` and the control's `aria-invalid`), focuses the first marked
 * control, and the footer names what is missing.
 *
 *   const v = useValidation();
 *   const nameError = nameProblem(...) ?? v.required(!name, "Enter a name.");
 *   <form ref={v.form} onSubmit={(e) => { e.preventDefault(); if (v.check(!blocked)) save(); }}>
 *   <Footer problem={v.problem(["a name"], !!nameError)} />
 *
 * Wrong values show as typed; missing ones only after a submit.
 */
export function useValidation() {
  // Failed submits so far; each one focuses the first marked field again.
  const [tries, setTries] = useState(0);
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (tries === 0) return;
    form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [tries]);
  const tried = tries > 0;
  return {
    form,
    tried,
    /** A missing field's message, once a submit was tried. */
    required: (empty: boolean, message: string) =>
      tried && empty ? message : undefined,
    /** Lets a submit go on when `ok`; otherwise marks the form. */
    check: (ok: boolean) => {
      if (!ok) setTries((t) => t + 1);
      return ok;
    },
    /**
     * The footer's line after a failed submit: what is still missing
     * (falsy entries are skipped), or a hint to fix the marked fields.
     */
    problem: (missing: readonly (string | false)[], wrong = false) => {
      if (!tried) return undefined;
      const need = missing.filter((m) => typeof m === "string");
      if (need.length > 0) return `Add ${list(need)} first.`;
      return wrong ? "Fix the marked fields first." : undefined;
    },
  };
}

/**
 * "✓ Saved" beside a setting that saves on change (a checkbox, a select),
 * once its save went through; it goes when the next save starts. `ok` is
 * the mutation's isSuccess for this control. Errors show where the
 * control shows them.
 */
export function Saved({ ok }: { ok: boolean }) {
  if (!ok) return null;
  return (
    <span role="status" className="text-xs text-ok">
      <span aria-hidden>✓</span> Saved
    </span>
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
 * The form dialogs' footer: the kit's DialogFoot with the CLI commands
 * that do the same, the error or why it can't submit yet on the left, and
 * Cancel and the named submit button on the right.
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
  className,
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
  /** A second way to submit, before the named one ("Create, don't deploy"). */
  alt?: ReactNode;
  /** The space above it, as DialogFoot's. */
  className?: string;
  onClose: () => void;
}) {
  return (
    <DialogFoot
      cli={cli}
      className={className}
      note={
        error ? (
          <ErrorText error={error} />
        ) : problem ? (
          <span className="text-fail">{problem}</span>
        ) : (
          disabled && !pending && <span className="text-faint">{reason}</span>
        )
      }
    >
      <Button onClick={onClose}>Cancel</Button>
      {alt}
      <button type="submit" className="btn pri" disabled={pending || disabled}>
        {submit} <kbd>↵</kbd>
      </button>
    </DialogFoot>
  );
}
