import type { ComponentProps, ReactNode } from "react";
import { Btn, ErrorText, Kbd } from "../../../src/features/data/kit";

// The pages before sign-in (login, first admin, invite and reset links):
// one narrow centered column.

/** The column with the product, a title and what it's for. */
export function Entry({
  title,
  context,
  children,
}: {
  title: string;
  context?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center px-4 py-10">
      <main className="flex w-full max-w-[340px] flex-col gap-5">
        <div className="flex flex-col gap-1">
          <span className="text-xs font-semibold text-(--g-mut)">
            tempoBase
          </span>
          <h1 className="text-lg font-semibold">{title}</h1>
          {context && <span className="text-xs text-(--g-mut)">{context}</span>}
        </div>
        {children}
      </main>
    </div>
  );
}

/** A labelled input. */
export function Field({
  label,
  hint,
  ...props
}: { label: string; hint?: ReactNode } & ComponentProps<"input">) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-xs text-(--g-dim)">{label}</span>
      <input className="g-field h-8" {...props} />
      {hint && <span className="text-xs text-(--g-mut)">{hint}</span>}
    </label>
  );
}

/** The form's one primary button and what went wrong. */
export function Submit({
  disabled,
  error,
  children,
}: {
  disabled?: boolean;
  error?: Error | null;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2">
      <Btn primary type="submit" disabled={disabled} className="h-8">
        {children} <Kbd>↵</Kbd>
      </Btn>
      <ErrorText error={error} />
    </div>
  );
}

/** At least 12 characters, as the server requires. */
export const minPassword = 12;
