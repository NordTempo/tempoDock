import { type ReactNode, useEffect, useRef } from "react";

/**
 * A modal window on the native <dialog>: focus trap, Esc to close and the
 * top layer come from the browser. Render it only while open; closing
 * (Esc, the backdrop, or the caller's own buttons) calls onClose, and the
 * caller unmounts it. Forms inside use their own submit button.
 */
export function Dialog({
  title,
  onClose,
  children,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
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
      className={`dlg m-auto w-full rounded-panel border border-edge bg-panel p-0 text-fg ${wide ? "max-w-[640px]" : "max-w-[440px]"}`}
    >
      <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
        <h2 className="text-[13px] font-semibold">{title}</h2>
        <button
          type="button"
          aria-label="Close"
          className="text-faint hover:text-fg"
          onClick={onClose}
        >
          esc
        </button>
      </div>
      <div className="px-4 py-3.5">{children}</div>
    </dialog>
  );
}
