import { useSyncExternalStore } from "react";

/**
 * Short confirmations that an action went through ("Restarted web"), shown
 * at the top right, under the header, for a few seconds. A module-level store, so any code
 * (a mutation's onSuccess) can call notify() without a provider. Static on
 * purpose: no enter or exit animation. Each notice holds one timeout until
 * it goes; nothing runs while none is shown.
 */
/** ok: done; info: started and still running; fail: didn't work. */
export type NoticeTone = "ok" | "info" | "fail";
/**
 * One follow-up a notice can carry ("See why" to a release): its label,
 * and what it does, usually a navigate().
 */
export type NoticeAction = { label: string; run: () => void };
type Notice = {
  id: number;
  text: string;
  tone: NoticeTone;
  action?: NoticeAction;
};

let notices: Notice[] = [];
let next = 1;
const listeners = new Set<() => void>();
const emit = () => {
  for (const l of listeners) l();
};

/** How long a notice stays, long enough to read a short sentence. */
const showMs = 4000;

/**
 * Shows text as a notice; at most three at once, the newest last. With an
 * action it stays twice as long, so the link can be reached.
 */
export function notify(
  text: string,
  tone: NoticeTone = "ok",
  action?: NoticeAction,
) {
  const id = next++;
  notices = [...notices.slice(-2), { id, text, tone, action }];
  emit();
  setTimeout(() => dismiss(id), action ? showMs * 2 : showMs);
}

function dismiss(id: number) {
  notices = notices.filter((n) => n.id !== id);
  emit();
}

/** Each tone's mark: a check when done, an arrow while it runs, a cross when it failed. */
const glyph: Record<NoticeTone, [string, string]> = {
  ok: ["✓", "text-ok"],
  info: ["→", "text-info"],
  fail: ["✕", "text-fail"],
};

/** The notices, for the shell to render once. */
export function Notices() {
  const list = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => notices,
  );
  return (
    <div
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed top-[72px] right-6 z-50 flex flex-col items-end gap-2"
    >
      {list.map((n) => (
        <div
          key={n.id}
          className="pointer-events-auto flex items-center gap-3 rounded-ctl border border-edge bg-panel py-1 pr-1 pl-3 text-[13px] text-fg shadow-lg shadow-black/60"
        >
          <span className="flex items-center gap-2 py-1">
            <span aria-hidden className={glyph[n.tone][1]}>
              {glyph[n.tone][0]}
            </span>
            {n.text}
          </span>
          {n.action && (
            <button
              type="button"
              className="text-info underline hover:text-fg"
              onClick={() => {
                dismiss(n.id);
                n.action?.run();
              }}
            >
              {n.action.label}
            </button>
          )}
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => dismiss(n.id)}
            className="grid size-[24px] place-items-center rounded-ctl text-mut hover:bg-hover hover:text-fg"
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
