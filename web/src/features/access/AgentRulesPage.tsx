import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { queries } from "../../data/queries";
import { ErrorText, Page } from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import { AgentMatrix } from "./AgentMatrix";
import { ShadowNote } from "./ShadowNote";

// Settings › Agent rules (ADR 0080, 6c): the most any of the
// organization's agents (people's `dock login` and its tokens) may use,
// per area role and environment kind, and where their changes ask an
// approver first. Trusted tokens (CI) can bypass them: a person sets
// that here; no token exempts itself.

export const agentRulesQuery = {
  queryKey: ["access", "agent-rules"],
  queryFn: () => body(api.GET("/api/v1/agent-rules")),
};

export function AgentRulesPage() {
  const saved = useSuspenseQuery(agentRulesQuery).data;
  const save = useAction(
    (settings: NonNullable<typeof saved.settings>) =>
      body(api.PUT("/api/v1/agent-rules", { body: { settings } })),
    () => [["access", "agent-rules"]],
    () => "Saved the agent rules",
  );
  return (
    <>
      <OrgSettingsBar />
      <Page>
        <ShadowNote />
        <AgentMatrix
          saved={saved.settings ?? []}
          pending={save.isPending}
          error={save.error}
          cli={(c) =>
            `dock agent rule ${c.area}=${c.level} --kinds ${c.kind}${c.ask ? " --ask" : " --no-ask"}`
          }
          onSave={(changed) => save.mutate(changed)}
        />
        <Bypass />
      </Page>
    </>
  );
}

/**
 * The organization's tokens, each with whether the rules apply to it.
 * Nothing for someone who may read the rules but not the tokens.
 */
function Bypass() {
  const tokens = (useQuery(queries.tokens()).data ?? []).filter(
    (t) => t.origin === "manual" && !t.revoked_at && !t.platform,
  );
  const set = useAction(
    ({ name, bypass }: { name: string; bypass: boolean }) =>
      body(
        api.PUT("/api/v1/tokens/{token}/rules-bypass", {
          params: { path: { token: name } },
          body: { bypass },
        }),
      ),
    () => [["tokens"], ["access"]],
    (v) =>
      v.bypass
        ? `The agent rules don't apply to ${v.name}`
        : `The agent rules apply to ${v.name} again`,
  );
  if (tokens.length === 0) return null;
  return (
    <>
      <h2 className="font-semibold">Bypass rules</h2>
      <table>
        <thead>
          <tr>
            <th>Token</th>
            <th>Scope</th>
            <th>Bypass rules</th>
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td className="py-1.5">{t.name}</td>
              <td className="py-1.5 text-mut">
                {t.scope}
                {t.project &&
                  ` on ${t.project}${t.environment ? `/${t.environment}` : ""}`}
              </td>
              <td className="py-0">
                {/* The label is the hit area: at least 24 px square. */}
                <label
                  className="inline-flex min-h-[24px] min-w-[24px] cursor-pointer items-center justify-center"
                  title={`dock token bypass ${t.name} ${t.rules_bypass ? "off" : "on"}`}
                >
                  <input
                    type="checkbox"
                    className="accent-fg"
                    aria-label={`${t.name} bypasses the agent rules`}
                    checked={!!t.rules_bypass}
                    disabled={set.isPending}
                    onChange={(e) =>
                      set.mutate({ name: t.name, bypass: e.target.checked })
                    }
                  />
                </label>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {set.error && <ErrorText error={set.error} />}
    </>
  );
}
