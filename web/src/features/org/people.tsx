import { Glyph } from "../../ui/kit";

// One row shape for people, shared by the organization's Members and the
// console's Users: name first with the email under it, and one two-factor
// indicator.

/** A person's initial in a circle; an invite gets an empty dashed one. */
export function Avatar({ name }: { name?: string }) {
  if (!name) {
    return (
      <span
        aria-hidden
        className="size-6 flex-none rounded-full border border-dashed border-dash"
      />
    );
  }
  return (
    <span
      aria-hidden
      className="grid size-6 flex-none place-items-center rounded-full bg-edge text-2xs font-semibold text-fg"
    >
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}

/** The person cell: avatar, name (or the email when unnamed), email under. */
export function PersonCell({
  name,
  email,
  you,
}: {
  name: string;
  email: string;
  you?: boolean;
}) {
  const shown = name.trim() || email;
  return (
    <span className="flex items-center gap-2.5">
      <Avatar name={shown} />
      <span className="min-w-0">
        <span className="font-medium">{shown}</span>
        {you && <span className="text-mut"> (you)</span>}
        {shown !== email && (
          <span className="block text-2xs text-mut">{email}</span>
        )}
      </span>
    </span>
  );
}

/**
 * Whether someone signs in with a second factor: ● on, or ○ off, in the
 * warning colour where it's required and they lack one (`note` says why).
 */
export function TwoFactorState({
  on,
  required,
  note,
}: {
  on: boolean;
  required?: boolean;
  note?: string;
}) {
  if (on) {
    return (
      <span className="text-ok">
        <Glyph char="●" /> on
      </span>
    );
  }
  return (
    <span className={required ? "text-warn" : "text-mut"} title={note}>
      <Glyph char="○" /> off
    </span>
  );
}
