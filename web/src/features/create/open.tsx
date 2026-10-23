import { lazy, Suspense, useSyncExternalStore } from "react";
import type { EnvRef } from "../../data/types";

/** Where a new service's code comes from; database means a template. */
export type ServiceSource = "image" | "source" | "github" | "database";

/** Which create dialog is open, and for what. */
export type CreateRequest =
  | { kind: "org" }
  | { kind: "project" }
  | { kind: "environment"; project: string; cloneFrom?: string }
  | { kind: "service"; env: EnvRef; source?: ServiceSource };

// The open dialog lives outside React so the sidebar, the palette and pages
// can all open one without a provider of their own.
let current: CreateRequest | null = null;
const listeners = new Set<() => void>();

function set(request: CreateRequest | null) {
  current = request;
  for (const listener of listeners) listener();
}

/** Opens a create dialog (ADR 0026). Its code loads on first use. */
export const openCreate = (request: CreateRequest) => set(request);

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

// Kept out of the shell chunk: the forms load when one opens.
const CreateDialog = lazy(() => import("./CreateDialog"));

/** Renders the open create dialog. The shell mounts it once. */
export function CreateHost() {
  const request = useSyncExternalStore(subscribe, () => current);
  if (!request) return null;
  return (
    <Suspense fallback={null}>
      <CreateDialog
        key={JSON.stringify(request)}
        request={request}
        onClose={() => set(null)}
      />
    </Suspense>
  );
}
