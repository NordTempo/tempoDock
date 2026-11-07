import type { EnvRef } from "./types";

/**
 * Values the person typed into variables this session, by environment,
 * service and key. A staged value is masked like any other, but the one
 * who just typed it can check it before Apply without revealing (and
 * auditing) the rest. Memory only: a reload forgets them.
 */
const typed = new Map<string, string>();

const id = (env: EnvRef, service: string | null | undefined, key: string) =>
  `${env.project}/${env.environment}/${service ?? ""}/${key}`;

/** Remembers what was staged; called by the stage mutation. */
export function rememberTyped(
  env: EnvRef,
  service: string | null | undefined,
  set: Record<string, string> | undefined,
) {
  for (const [key, value] of Object.entries(set ?? {})) {
    typed.set(id(env, service, key), value);
  }
}

/** The value this session staged for a variable, if it did. */
export const typedValue = (
  env: EnvRef,
  service: string | null | undefined,
  key: string,
) => typed.get(id(env, service, key));
