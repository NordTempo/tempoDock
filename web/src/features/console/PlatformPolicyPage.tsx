import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { Button, ErrorText, Page, PageBar } from "../../ui/kit";

// The platform's data access default and ceiling (ADR 0062): what applies
// where an organization set nothing, and the most any organization's rule
// or token gives. Organizations set their own rules within it, in their
// settings. Org admins aren't capped.

type Policy = components["schemas"]["PlatformDataPolicy"];
type Rule = components["schemas"]["DataPolicyRule"];
type Level = Rule["level"];

export const platformPolicyQuery = () => ({
  queryKey: ["console", "data-policy"] as const,
  queryFn: () => body(api.GET("/api/v1/console/data-policy")),
});

const useSavePlatformPolicy = () =>
  useAction(
    (p: Policy) => body(api.PUT("/api/v1/console/data-policy", { body: p })),
    () => [["console", "data-policy"], ["data-policy"], ["audit"]],
    () => "Saved the platform's data policy",
  );

const subjects: { subject: Rule["subject"]; label: string }[] = [
  { subject: "role:read", label: "Read members" },
  { subject: "role:deploy", label: "Deploy members" },
  { subject: "tokens", label: "Tokens" },
];
const kinds: { kind: Rule["kind"]; label: string }[] = [
  { kind: "production", label: "production" },
  { kind: "non_production", label: "non-production" },
];
const levels: Level[] = ["none", "schema", "read", "propose", "write"];

/** "" for not set, else the level. */
type Cell = { level: Level | ""; sensitive: Rule["sensitive"] };

const key = (r: Pick<Rule, "subject" | "kind">) => `${r.subject} ${r.kind}`;

function toCells(rules: Rule[]): Record<string, Cell> {
  return Object.fromEntries(
    rules.map((r) => [key(r), { level: r.level, sensitive: r.sensitive }]),
  );
}

function toRules(cells: Record<string, Cell>): Rule[] {
  return subjects.flatMap(({ subject }) =>
    kinds.flatMap(({ kind }) => {
      const c = cells[key({ subject, kind })];
      return c?.level
        ? [{ subject, kind, level: c.level, sensitive: c.sensitive }]
        : [];
    }),
  );
}

export function PlatformPolicyPage() {
  const { data } = useSuspenseQuery(platformPolicyQuery());
  const save = useSavePlatformPolicy();
  const [defaults, setDefaults] = useState(() => toCells(data.defaults ?? []));
  const [ceilings, setCeilings] = useState(() => toCells(data.ceilings ?? []));
  const builtIn = toCells(data.built_in ?? []);
  const cell = (
    which: Record<string, Cell>,
    set: (next: Record<string, Cell>) => void,
    k: string,
    none: string,
    name: string,
  ) => {
    const c = which[k] ?? { level: "", sensitive: "masked" };
    const update = (next: Cell) => set({ ...which, [k]: next });
    return (
      <span className="flex items-center gap-1.5">
        <select
          aria-label={name}
          className="field min-h-[24px] py-0.5 text-xs"
          value={c.level}
          onChange={(e) => update({ ...c, level: e.target.value as Level })}
        >
          <option value="">{none}</option>
          {levels.map((l) => (
            <option key={l} value={l}>
              {l}
            </option>
          ))}
        </select>
        {c.level && (
          <select
            aria-label="Sensitive columns"
            className="field min-h-[24px] py-0.5 text-xs"
            value={c.sensitive}
            onChange={(e) =>
              update({ ...c, sensitive: e.target.value as Rule["sensitive"] })
            }
          >
            <option value="masked">masked</option>
            <option value="visible">visible</option>
          </select>
        )}
      </span>
    );
  };
  return (
    <>
      <PageBar title="Data access" />
      <Page>
        <p className="max-w-2xl text-xs text-sec">
          The platform's default applies where an organization set nothing; the
          ceiling is the most any organization's rule or token gives. Each
          organization sets its own rules in its settings. Organization admins
          always have full access.
        </p>
        <div className="overflow-x-auto border-t border-line">
          <table>
            <thead>
              <tr>
                <th>Who</th>
                <th>In</th>
                <th>Default</th>
                <th>Ceiling</th>
              </tr>
            </thead>
            <tbody>
              {subjects.flatMap(({ subject, label }) =>
                kinds.map(({ kind, label: kindLabel }) => {
                  const k = key({ subject, kind });
                  const b = builtIn[k];
                  return (
                    <tr key={k}>
                      <td className="py-2">{label}</td>
                      <td className="py-2 text-mut">{kindLabel}</td>
                      <td className="py-2">
                        {cell(
                          defaults,
                          setDefaults,
                          k,
                          `built-in (${b?.level ?? "none"})`,
                          `Default for ${label} in ${kindLabel}`,
                        )}
                      </td>
                      <td className="py-2">
                        {cell(
                          ceilings,
                          setCeilings,
                          k,
                          "no ceiling",
                          `Ceiling for ${label} in ${kindLabel}`,
                        )}
                      </td>
                    </tr>
                  );
                }),
              )}
            </tbody>
          </table>
        </div>
        <div className="flex items-center gap-3">
          <Button
            primary
            disabled={save.isPending}
            onClick={() =>
              save.mutate({
                defaults: toRules(defaults),
                ceilings: toRules(ceilings),
                built_in: [], // read-only: the server's own
              })
            }
          >
            Save
          </Button>
          <ErrorText error={save.error} />
        </div>
      </Page>
    </>
  );
}
