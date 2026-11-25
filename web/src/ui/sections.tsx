import type { ReactNode } from "react";

/**
 * A section on a settings page: its heading (with actions on the right)
 * and its rows. `id` is its anchor: the address's #hash and search's
 * results (serviceSettingSections) find it.
 */
export function SettingsSection({
  id,
  title,
  actions,
  children,
}: {
  id: string;
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      // 12px under the sticky header: html's scroll-padding-top
      // (index.css) already keeps it clear of the header.
      className="scroll-mt-[12px]"
    >
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id={`${id}-h`} className="text-lg font-semibold">
          {title}
        </h2>
        {actions && (
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {actions}
          </div>
        )}
      </div>
      {children}
    </section>
  );
}

/**
 * A setting on a project's Settings and Project settings (ADR 0099):
 * one row per setting, its name
 * on the left and what it is, with its buttons, beside it. `id` is the
 * address's #hash.
 */
export function SettingRow({
  id,
  label,
  danger,
  children,
}: {
  id: string;
  label: ReactNode;
  /** The row that archives or deletes: its name in red. */
  danger?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      className="grid scroll-mt-[12px] gap-x-6 gap-y-2 border-b border-line py-3 first:border-t md:grid-cols-[170px_minmax(0,1fr)]"
    >
      <h2
        id={`${id}-h`}
        className={`font-semibold ${danger ? "text-fail" : ""}`}
      >
        {label}
      </h2>
      <div className="min-w-0">{children}</div>
    </section>
  );
}
