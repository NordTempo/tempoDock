import { lazy, Suspense, useSyncExternalStore } from "react";
import type { ServiceRef } from "../data/types";

/**
 * An action the palette asks about before it runs, with
 * the same confirm as its button: a restart, or a rollback to a release.
 */
export type ConfirmRequest =
  | { kind: "restart"; ref: ServiceRef }
  | { kind: "rollback"; ref: ServiceRef; to: number };

// Outside React, like openCreate: commands open it without a provider.
let current: ConfirmRequest | null = null;
const listeners = new Set<() => void>();

function set(request: ConfirmRequest | null) {
  current = request;
  for (const listener of listeners) listener();
}

/** Opens the confirm for a palette action; its code loads on first use. */
export const openConfirm = (request: ConfirmRequest) => set(request);

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

const ActionConfirm = lazy(() => import("./ActionConfirm"));

/** Renders the open action confirm. The palette provider mounts it once. */
export function ConfirmHost() {
  const request = useSyncExternalStore(subscribe, () => current);
  if (!request) return null;
  return (
    <Suspense fallback={null}>
      <ActionConfirm
        key={JSON.stringify(request)}
        request={request}
        onClose={() => set(null)}
      />
    </Suspense>
  );
}
