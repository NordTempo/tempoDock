import { type ReactNode, useEffect, useRef } from "react";

/**
 * A modal window on the native <dialog>: focus trap, Esc to close and the
 * top layer come from the browser; the element marked autofocus, else
 * the first field, gets focus. Render it only while open; closing
 * (Esc, the backdrop, or the caller's own buttons) calls onClose, and the
 * caller unmounts it. Forms inside use their own submit button.
 * `bare` drops the title bar and padding for windows that lay out their
 * own (the command palette); `className` then places and sizes it.
 */
export function Dialog({
  title,
  onClose,
  children,
  wide,
  bare,
  className,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
  bare?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  // React's autoFocus runs before showModal, which then focuses the first
  // focusable element (the close button). Focus the first field instead.
  useEffect(() => {
    const d = ref.current;
    if (!d || d.open) return;
    d.showModal();
    (
      d.querySelector<HTMLElement>("[autofocus], [data-autofocus]") ??
      d.querySelector<HTMLElement>("input:not([type=hidden]), select, textarea")
    )?.focus();
  }, []);
  return (
    <dialog
      ref={ref}
      aria-label={title}
      onClose={onClose}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      // A click on the backdrop lands on the <dialog> itself.
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
      className={`dlg w-full rounded-panel border border-edge bg-panel p-0 text-left whitespace-normal text-fg ${className ?? `m-auto ${wide ? "max-w-[640px]" : "max-w-[440px]"}`}`}
    >
      {bare ? (
        children
      ) : (
        <>
          <div className="flex items-center justify-between border-b border-line px-5 py-3">
            <h2 className="text-md font-semibold">{title}</h2>
            <button
              type="button"
              aria-label="esc, close"
              className="rounded-ctl p-0.5 text-faint hover:text-fg"
              onClick={onClose}
            >
              <kbd>esc</kbd>
            </button>
          </div>
          <div className="px-5 py-4">{children}</div>
        </>
      )}
    </dialog>
  );
}
