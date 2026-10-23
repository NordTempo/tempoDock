import type { Variable } from "../../data/types";

/** Matches ${{service.KEY}} and ${{shared.KEY}}, like the daemon. */
const refRe = /\$\{\{\s*([a-z0-9-]+)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g;

export const hasReference = (value: string) => /\$\{\{/.test(value);

/**
 * Expands references the way the daemon does at deploy time, from revealed
 * values: `shared.KEY` is an environment variable, `web.KEY` one of web's.
 * Unknown references stay as written, and so do ${{org.KEY}}: their values
 * are the organization's.
 */
export function resolve(
  value: string,
  variables: Variable[],
  depth = 0,
): string {
  if (depth > 8) return value;
  return value.replace(refRe, (whole, scope: string, key: string) => {
    const target = variables.find((v) =>
      scope === "shared"
        ? v.scope === "environment" && v.key === key
        : v.scope === "service" && v.service === scope && v.key === key,
    );
    return target?.value === undefined
      ? whole
      : resolve(target.value, variables, depth + 1);
  });
}

/** Hides URL passwords: postgres://shop:secret@db → postgres://shop:••••@db. */
export const maskUrlPasswords = (value: string) =>
  value.replace(/(\/\/[^:/@\s]+:)[^@\s]+@/g, "$1••••@");
