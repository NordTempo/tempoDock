import { lazy, Suspense, useEffect, useState } from "react";
import { registerReauthPrompt } from "../api/reauth";

// Kept out of the initial bundle: the form loads when a step-up asks.
const ReauthDialog = lazy(() => import("../features/account/ReauthDialog"));

/**
 * Shows "Confirm it's you" when a sensitive call asks for it (ADR 0048,
 * api/reauth.ts). Mounted once next to the router, so every page gets
 * it, the CLI login page included.
 */
export function ReauthHost() {
  const [answer, setAnswer] = useState<(ok: boolean) => void>();
  useEffect(
    () =>
      registerReauthPrompt(
        () => new Promise<boolean>((resolve) => setAnswer(() => resolve)),
      ),
    [],
  );
  if (!answer) return null;
  const done = (ok: boolean) => {
    setAnswer(undefined);
    answer(ok);
  };
  return (
    <Suspense fallback={null}>
      <ReauthDialog onDone={done} />
    </Suspense>
  );
}
