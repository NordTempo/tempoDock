import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// Dock's own email (ADR 0079, `dock email`): the platform's SMTP server
// and the mail sent through it. Operators only. applyEmailEvent is in the
// initial bundle (data/live.ts), so this file stays small.

type Schemas = components["schemas"];
export type EmailSettings = Schemas["EmailSettings"];
export type EmailDelivery = Schemas["EmailDelivery"];
export type InviteMail = Schemas["InviteMail"];

const emailKey = ["platform", "email"];
const outboxKey = [...emailKey, "outbox"];

/** How many mails the console lists. */
const shown = 20;

export const emailQuery = () =>
  queryOptions({
    queryKey: emailKey,
    queryFn: () => body(api.GET("/api/v1/platform/email")),
  });

export const outboxQuery = () =>
  queryOptions({
    queryKey: outboxKey,
    queryFn: () =>
      body(
        api.GET("/api/v1/platform/email/outbox", {
          params: { query: { limit: shown } },
        }),
      ),
  });

/** Sets the SMTP server; a password left out stays. */
export const useSetEmail = () =>
  useAction(
    (req: Schemas["SetEmailRequest"]) =>
      body(api.PUT("/api/v1/platform/email", { body: req })),
    () => [emailKey, ["audit"]],
    () => "Saved the email settings",
  );

/** Turns email off. */
export const useRemoveEmail = () =>
  useAction(
    () => body(api.DELETE("/api/v1/platform/email")),
    () => [emailKey, ["audit"]],
    () => "Email is off",
  );

/** Sends a test mail to the operator's own address. */
export const useTestEmail = () =>
  useAction(
    () => body(api.POST("/api/v1/platform/email/test", { body: {} })),
    () => [emailKey, ["audit"]],
    (_, r) => (r.ok ? `Sent a test email to ${r.to}` : ["Test failed", "fail"]),
  );

/**
 * A mail was queued, sent, retried or given up on: patch the console's
 * list, refresh the settings' counts, and patch the invite it carries,
 * in any cached list of invites (the org's Members, the console's).
 */
export function applyEmailEvent(qc: QueryClient, d: EmailDelivery) {
  qc.setQueryData<Schemas["EmailLog"]>(outboxKey, (log) => {
    if (!log) return log;
    const rest = (log.deliveries ?? []).filter((x) => x.id !== d.id);
    return { deliveries: [d, ...rest].slice(0, shown) };
  });
  qc.invalidateQueries({ queryKey: emailKey, exact: true });
  const invite = d.invite;
  if (!invite) return;
  const mail: InviteMail = {
    status: d.status,
    id: d.id,
    error: d.error,
    at: d.done_at,
  };
  qc.setQueriesData<Schemas["Invite"][]>(
    { predicate: (q) => q.queryKey.at(-1) === "invites" },
    (list) =>
      Array.isArray(list)
        ? list.map((i) => (i.id === invite ? { ...i, mail } : i))
        : list,
  );
}
