import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { keys, servicePath } from "../../data/queries";
import type { Release, ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, plural } from "../../ui/format";
import { Prop, SectionHead } from "../../ui/kit";
import type { TextSettingKey } from "../settings/settings";

// A release's diagnosis (ADR 0071) on its page, why it failed or why it
// crash looped or turned unhealthy while live: the likely cause
// with what was ruled out, then the evidence as rows, the rows a rule
// matched marked. Dock captured it when the release failed; the rules run
// in core, so the UI, `dock diagnose` and MCP say the same.

export type Diagnosis = components["schemas"]["Diagnosis"];
type Fix = components["schemas"]["DiagnosisFix"];

/** A diagnosis never changes once captured. */
const diagnosisQuery = (ref: ServiceRef, release: number, id: number) =>
  queryOptions({
    queryKey: [...keys.release(ref, release), "diagnosis", id],
    queryFn: () =>
      body(
        api.GET(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/diagnoses/{id}",
          { params: { path: { ...servicePath(ref), id } } },
        ),
      ),
    staleTime: Number.POSITIVE_INFINITY,
  });

/** The release's diagnosis once loaded; undefined without one. */
export function useDiagnosis(ref: ServiceRef, release: Release) {
  const id = release.diagnosis?.id;
  return useQuery({
    ...diagnosisQuery(ref, release.number, id ?? 0),
    enabled: id !== undefined,
  }).data;
}

/** The settings the release page edits in place, and the others' editors. */
export function fixEdit(
  fix: Fix | undefined,
):
  | { inPlace: "port" | "health_check_path"; label: string }
  | { setting: TextSettingKey; label: string }
  | { variables: string; label: string }
  | undefined {
  switch (fix?.setting) {
    case "port":
      return {
        inPlace: "port",
        label: fix.suggested
          ? `Change port to ${fix.suggested}`
          : "Change port",
      };
    case "health_check_path":
      return {
        inPlace: "health_check_path",
        label: "Change health check path",
      };
    case "memory":
      return {
        setting: "memory_mb",
        label: fix.suggested
          ? `Raise memory to ${fix.suggested} MB`
          : "Raise memory",
      };
    case "start_command":
      return { setting: "start_command", label: "Change start command" };
    case "health_check_timeout":
      return {
        setting: "health_check_timeout_seconds",
        label: "Change health check timeout",
      };
    case "pre_deploy_command":
      return {
        setting: "pre_deploy_command",
        label: "Change pre-deploy command",
      };
    case "image":
      return { setting: "image", label: "Change image" };
    case "variables":
      return {
        variables: fix.suggested ?? "",
        label: fix.suggested ? `Set ${fix.suggested}` : "Variables",
      };
  }
  return undefined;
}

/** The likely cause, what matched, and what was ruled out. */
export function Cause({ d }: { d: Diagnosis }) {
  const ruledOut = (d.checked ?? []).filter((c) => !c.matched);
  const running =
    d.kind === "crash_loop"
      ? "Crash looped"
      : d.kind === "unhealthy"
        ? "Turned unhealthy"
        : undefined;
  return (
    <div className="border-l-2 border-fail py-1.5 pl-3.5">
      {running && (
        <p className="text-xs text-mut">
          {running} {ago(d.captured_at)}, while live
        </p>
      )}
      <p className="text-md font-semibold">
        {d.cause.rule ? d.cause.summary : "No rule matched"}
      </p>
      {d.cause.rule && d.cause.detail && (
        <p className="mt-0.5 text-xs break-all text-mut">
          Matched: <span className="font-mono">{d.cause.detail}</span>
        </p>
      )}
      {ruledOut.length > 0 && (
        <p className="mt-0.5 text-xs text-faint">
          Ruled out:{" "}
          {ruledOut
            .map((c) => `${c.rule.replaceAll("_", " ")} (${c.why})`)
            .join(", ")}
          .
        </p>
      )}
    </div>
  );
}

const mark = <span className="mr-1.5 font-mono text-xs text-fail">●</span>;

/** The evidence: one row per signal, the matched ones marked. */
export function Evidence({ d, ref }: { d: Diagnosis; ref: ServiceRef }) {
  const org = useOrg();
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const matched = (d.container_tail ?? []).filter((l) => l.matched);
  const health = d.health;
  const rule = d.cause.rule ?? "";
  const healthHit = [
    "port_mismatch",
    "loopback_only",
    "health_path",
    "not_listening",
  ].includes(rule);
  const exit = d.failure.exit;
  const changes = d.changes;
  const res = d.resources;
  return (
    <section aria-labelledby="diagnosis" className="flex flex-col gap-2">
      <SectionHead
        id="diagnosis"
        aside={
          <span className="font-mono">
            dock diagnose -r {d.release} {flags}
          </span>
        }
      >
        Diagnosis
      </SectionHead>
      <dl className="props grid-cols-[150px_minmax(0,1fr)]">
        {health && (
          <Prop label="Health check">
            {healthHit && mark}
            {healthText(health)}
          </Prop>
        )}
        {matched.length > 0 && (
          <Prop label="Output">
            {matched.map((l) => (
              <div key={l.text} className="font-mono text-xs break-all">
                {mark}
                {l.text}
              </div>
            ))}
          </Prop>
        )}
        {exit && (
          <Prop label="Container">
            {rule === "oom_killed" && mark}
            {exitText(exit)}
          </Prop>
        )}
        {changes && (
          <Prop label={`Changed since r${d.last_good_release}`}>
            {changesText(changes)}
          </Prop>
        )}
        {d.pre_deploy && (
          <Prop label="Pre-deploy">
            {rule === "pre_deploy_failed" && mark}
            <span className="font-mono">{d.pre_deploy.command}</span>{" "}
            {preDeployText(d.pre_deploy)}
          </Prop>
        )}
        <Prop label="Errors">
          {(d.errors ?? []).length === 0 ? (
            <span className="text-mut">None around the failure</span>
          ) : (
            <Link
              to="/$org/$project/$environment/$service/errors"
              params={{ org, ...ref }}
              className="text-info"
            >
              {(d.errors ?? []).map((e) => e.title).join(" · ")}
            </Link>
          )}
        </Prop>
        {res && (res.memory_mb ?? []).length > 0 && (
          <Prop label="Resources">{resourcesText(res)}</Prop>
        )}
        {d.trace && (
          <Prop label="Failed trace">
            <Link
              to="/$org/$project/$environment/$service/requests/traces/$trace"
              params={{ org, ...ref, trace: d.trace.id }}
              search={{ at: d.trace.at }}
              className="text-info"
            >
              {d.trace.name}
              {d.trace.http_status ? ` ${d.trace.http_status}` : ""}
            </Link>{" "}
            <span className="text-mut">
              {Math.round(d.trace.duration_ms)} ms
            </span>
          </Prop>
        )}
      </dl>
      {d.truncated && (
        <p className="text-xs text-faint">
          Older lines were left out to keep the diagnosis small; the full log
          has them.
        </p>
      )}
    </section>
  );
}

function healthText(h: NonNullable<Diagnosis["health"]>) {
  const what =
    h.kind === "http"
      ? `GET ${h.path} on :${h.port}`
      : h.kind === "tcp"
        ? `TCP on :${h.port}`
        : "it had to keep running";
  const got = h.status
    ? `answered ${h.status}`
    : h.result
      ? h.result.replaceAll("_", " ")
      : "failed";
  return `${what} for ${h.timeout_seconds}s: ${got}`;
}

function exitText(e: NonNullable<Diagnosis["failure"]["exit"]>) {
  const restarts = plural(e.restarts, "restart");
  if (e.oom_killed)
    return `Killed for memory, limit ${e.memory_limit_mb} MB, ${restarts}`;
  if (e.running && e.exit_code != null)
    return `Running again after exit ${e.exit_code}, ${restarts}`;
  if (e.running) return `Still running when the deploy stopped it, ${restarts}`;
  return `Exited with code ${e.exit_code}, ${restarts}`;
}

function changesText(c: NonNullable<Diagnosis["changes"]>) {
  const parts: string[] = [];
  if (c.image) {
    const commit = c.image.commit
      ? ` (${c.image.commit.sha.slice(0, 7)} ${c.image.commit.message})`
      : "";
    parts.push(`image ${c.image.from} → ${c.image.to}${commit}`);
  }
  const v = c.variables;
  for (const [names, what] of [
    [v.added, "added"],
    [v.removed, "removed"],
    [v.changed, "changed"],
  ] as const) {
    if ((names ?? []).length > 0) parts.push(`${names?.join(", ")} ${what}`);
  }
  for (const s of c.settings ?? [])
    parts.push(`${s.name.replaceAll("_", " ")} ${s.from} → ${s.to}`);
  return parts.length > 0
    ? parts.join(" · ")
    : "Same image, settings and variables";
}

function preDeployText(p: NonNullable<Diagnosis["pre_deploy"]>) {
  const took = `${(p.duration_ms / 1000).toFixed(1)}s`;
  if (p.timed_out) return `timed out after ${took}`;
  if (p.exit_code === null || p.exit_code === undefined) return "couldn't run";
  return `exit ${p.exit_code}, ${took}`;
}

function resourcesText(r: NonNullable<Diagnosis["resources"]>) {
  const mem = r.memory_mb ?? [];
  const cpu = r.cpu_percent ?? [];
  const last = mem.length - 1;
  const limit = r.memory_limit_mb ? ` of ${r.memory_limit_mb} MB` : "";
  const of = r.of_release ? `r${r.of_release} at ` : "";
  return `${of}${mem[last]} MB${limit} (peak ${r.peak_memory_mb} MB), CPU ${Math.round(cpu[last] ?? 0)}%`;
}
