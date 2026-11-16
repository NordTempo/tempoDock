import { useEffect, useRef, useState } from "react";
import type { ServiceRef } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { Act, ErrorText } from "../../ui/kit";
import { useCancelRelease } from "./data";

/**
 * Cancel for a release that is deploying (ADR 0086). "Cancel rN" turns
 * into "Cancel rN? Yes · No" where it is, in every environment; Yes stops
 * the deploy before traffic moves to it. The question goes away by
 * itself after a while. With `shortcut`, X asks too (the service header).
 */
export function CancelDeploy({
  serviceRef: ref,
  number,
  shortcut,
}: {
  serviceRef: ServiceRef;
  number: number;
  shortcut?: boolean;
}) {
  const cancel = useCancelRelease();
  const [asking, setAsking] = useState(false);
  const no = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!asking) return;
    no.current?.focus(); // the safe answer
    const timer = setTimeout(() => setAsking(false), 8000);
    return () => clearTimeout(timer);
  }, [asking]);
  useShortcuts(
    shortcut
      ? [
          {
            key: "x",
            label: "cancel deploy",
            disabled: cancel.isPending,
            run: () => setAsking(true),
          },
        ]
      : [],
  );
  const r = `r${number}`;
  const cli = `dock release cancel ${number} -s ${ref.service} -e ${ref.environment}`;
  const error = <ErrorText error={cancel.error} />;

  if (cancel.isPending)
    return (
      <Act cli={cli} disabled>
        Canceling {r}
      </Act>
    );
  if (!asking)
    return (
      <>
        <Act
          cli={cli}
          title={`Stop ${r} before traffic moves to it; the serving release keeps serving`}
          onClick={() => {
            cancel.reset();
            setAsking(true);
          }}
        >
          Cancel {r} {shortcut && <kbd>X</kbd>}
        </Act>
        {error}
      </>
    );
  return (
    // The trigger stays neutral (kit rule); the answer that stops the
    // deploy is the red one. The answers' 24px targets overlap the
    // padding (-my-1), so the row keeps its height.
    <fieldset
      aria-label={`Cancel ${r}?`}
      className="btn m-0 gap-2.5"
      onKeyDown={(e) => {
        if (e.key === "Escape") setAsking(false);
      }}
    >
      Cancel {r}?
      <button
        type="button"
        className="-my-1 font-semibold text-fail underline decoration-fail-edge underline-offset-2 hover:decoration-current"
        onClick={() =>
          cancel.mutate({ ref, number }, { onSettled: () => setAsking(false) })
        }
      >
        Yes
      </button>
      <span className="text-faint">·</span>
      <button
        ref={no}
        type="button"
        className="-my-1 underline decoration-faint underline-offset-2 hover:decoration-current"
        onClick={() => setAsking(false)}
      >
        No
      </button>
    </fieldset>
  );
}
