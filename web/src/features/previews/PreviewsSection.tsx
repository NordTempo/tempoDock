import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { canDeploy, useSession } from "../../data/queries";
import type { EnvRef, Session } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { httpsHref } from "../../ui/httpsHref";
import { Act, Button, Check, ErrorText, StateText } from "../../ui/kit";
import { autofocus } from "../create/form";
import {
  type EnvironmentPreviews,
  minutes,
  previewDataText,
  previewQueries,
  previewsCli,
  useSetEnvironmentPreviews,
} from "./data";
import { previewStates } from "./PreviewParts";

/** Admin of an environment, as core checks it for preview settings. */
export const canAdmin = (s: Session | null, env: EnvRef) =>
  s?.scope === "admin" && canDeploy(s, env);

/**
 * The project's Previews (ADR 0056, 0077), a summary: the services that
 * build pull requests (their switch is in each service's Settings ›
 * Source), the numbers of each environment previews are cloned from,
 * edited here, and the open previews.
 */
export function PreviewsSection({ project }: { project: string }) {
  const org = useOrg();
  const settings = useQuery(previewQueries.settings(project));
  const s = settings.data;
  return (
    <div className="flex flex-col gap-5">
      <section aria-labelledby="preview-services" className="flex flex-col">
        <h3 id="preview-services" className="mb-1.5 font-semibold">
          Services that build pull requests
        </h3>
        {settings.error && <ErrorText error={settings.error} />}
        {s && s.services.length === 0 && (
          <>
            <p className="text-mut">
              None. Turn previews on for a service connected to GitHub in its
              Settings › Source, or with:
            </p>
            <code className="mt-1 font-mono text-xs text-sec">
              dock service previews on -s web -e staging
            </code>
          </>
        )}
        {s && s.services.length > 0 && (
          <ul className="border-line border-t">
            {s.services.map((sp) => (
              <li
                key={`${sp.environment}/${sp.service}`}
                className="border-soft border-b"
              >
                <Link
                  to="/$org/$project/$environment/$service/settings"
                  params={{
                    org,
                    project,
                    environment: sp.environment,
                    service: sp.service,
                  }}
                  className="grid grid-cols-[110px_110px_minmax(0,1fr)_130px_minmax(0,160px)_14px] items-baseline gap-3 px-1 py-2 text-sec no-underline hover:bg-hover"
                >
                  <span className="truncate font-mono text-fg">
                    {sp.service}
                  </span>
                  <span className="truncate text-mut">{sp.environment}</span>
                  <span className="truncate font-mono">
                    {sp.repo ? (
                      <>
                        {sp.repo} <span className="text-faint">·</span>{" "}
                        {sp.branch}
                        {sp.dir && (
                          <>
                            {" "}
                            <span className="text-faint">·</span> {sp.dir}
                          </>
                        )}
                      </>
                    ) : sp.data !== "empty" ? (
                      <span className="font-sans">
                        starts with its {previewDataText(sp.data)}
                      </span>
                    ) : (
                      <span className="font-sans text-mut">
                        runs its current image
                      </span>
                    )}
                  </span>
                  <span className={sp.build ? "text-fg" : "text-mut"}>
                    {sp.build
                      ? sp.allow_forks
                        ? "builds, forks too"
                        : "builds"
                      : "doesn't build"}
                  </span>
                  <span className="truncate font-mono text-mut text-xs">
                    {sp.copy_variables.join(", ")}
                  </span>
                  <span aria-hidden className="text-faint">
                    ›
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
        {s?.environments.map((e) => (
          <EnvironmentPreviewsRow
            key={e.environment}
            env={{ project, environment: e.environment }}
            numbers={e}
          />
        ))}
      </section>

      <section aria-labelledby="preview-open" className="flex flex-col">
        <h3 id="preview-open" className="mb-1.5 font-semibold">
          Open previews
        </h3>
        {s && s.previews.length === 0 && (
          <p className="text-mut">No open previews.</p>
        )}
        {s && s.previews.length > 0 && (
          <ul className="border-line border-t">
            {s.previews.map((pv) => (
              <li
                key={pv.environment}
                className="grid grid-cols-[80px_minmax(0,1fr)_90px_100px_minmax(0,1fr)_60px] items-baseline gap-3 border-soft border-b px-1 py-2"
              >
                <Link
                  to="/$org/$project/$environment"
                  params={{ org, project, environment: pv.environment }}
                  className="font-mono"
                >
                  {pv.environment}
                </Link>
                <a
                  href={httpsHref(pv.pull_url)}
                  target="_blank"
                  rel="noreferrer"
                  className="truncate text-sec no-underline hover:text-fg"
                >
                  <span className="font-mono text-info">#{pv.number}</span>{" "}
                  {pv.title}
                </a>
                <span className="truncate text-mut">{pv.author}</span>
                <StateText state={previewStates[pv.status]} />
                <span className="truncate font-mono text-xs">
                  {pv.urls[0] && (
                    <a
                      href={pv.urls[0].url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-info"
                    >
                      {pv.urls[0].url.replace(/^https?:\/\//, "")}
                    </a>
                  )}
                </span>
                <span className="text-faint text-xs">{ago(pv.updated_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

/**
 * One base environment's numbers on a line, with Edit: how many previews
 * may be open, when they sleep, and the shared values they copy.
 */
function EnvironmentPreviewsRow({
  env,
  numbers,
}: {
  env: EnvRef;
  numbers: EnvironmentPreviews;
}) {
  const admin = canAdmin(useSession(), env);
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <EnvironmentPreviewsForm
        env={env}
        numbers={numbers}
        onDone={() => setEditing(false)}
      />
    );
  }
  return (
    <p className="mt-2.5 flex max-w-[860px] flex-wrap items-center gap-x-3 gap-y-1">
      <span className="min-w-0 flex-1">
        <span className="font-mono">{env.environment}</span>
        <span className="text-mut"> · at most </span>
        {numbers.max_previews}
        <span className="text-mut"> open · </span>
        {numbers.sleep_after_minutes > 0 ? (
          <>
            <span className="text-mut">sleep after </span>
            {minutes(numbers.sleep_after_minutes)}
          </>
        ) : (
          <span className="text-mut">never sleep</span>
        )}
        {numbers.copy_shared.length > 0 && (
          <>
            <span className="text-mut"> · copies shared </span>
            <span className="font-mono">{numbers.copy_shared.join(", ")}</span>
          </>
        )}
      </span>
      {admin && (
        <Button small onClick={() => setEditing(true)}>
          Edit
        </Button>
      )}
    </p>
  );
}

/** The sleep choices, in minutes; 0 never. */
const sleepChoices = [0, 15, 30, 60, 120, 360, 1440];

/** Edits a base environment's numbers (`dock env previews set`). */
function EnvironmentPreviewsForm({
  env,
  numbers,
  onDone,
}: {
  env: EnvRef;
  numbers: EnvironmentPreviews;
  onDone: () => void;
}) {
  const set = useSetEnvironmentPreviews();
  const [max, setMax] = useState(String(numbers.max_previews));
  const [sleep, setSleep] = useState(numbers.sleep_after_minutes);
  const [never, setNever] = useState(numbers.sleep_after_minutes === 0);
  const [copies, setCopies] = useState(numbers.copy_shared.join(", "));
  const keys = copies.split(/[\s,]+/).filter(Boolean);
  const maxN = Number(max);
  const maxOk = Number.isInteger(maxN) && maxN >= 1 && maxN <= 20;
  const cli = previewsCli(
    `set ${env.environment} --max ${max || 3} --sleep-after ${never ? "off" : `${sleep}m`}${keys.map((k) => ` --copy ${k}`).join("")}`,
    env.project,
  );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!maxOk) return;
    set.mutate(
      {
        env,
        req: {
          max_previews: maxN,
          sleep_after_minutes: never ? -1 : sleep || 30,
          copy_shared: keys,
        },
      },
      { onSuccess: onDone },
    );
  };
  const choices =
    sleepChoices.includes(sleep) || sleep === 0
      ? sleepChoices
      : [...sleepChoices, sleep].sort((a, b) => a - b);
  return (
    <form
      aria-label={`Previews cloned from ${env.environment}`}
      onSubmit={submit}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
      className="mt-2.5 flex max-w-[860px] flex-col gap-2.5 border-line border-y py-3"
    >
      <span className="font-mono">{env.environment}</span>
      <div className="grid grid-cols-[140px_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
        <label htmlFor="pv-max" className="text-mut">
          Open at most
        </label>
        <input
          id="pv-max"
          ref={autofocus}
          value={max}
          inputMode="numeric"
          onChange={(e) => setMax(e.target.value)}
          aria-invalid={!maxOk}
          className="field w-20 py-1 font-mono text-xs"
        />
        <span className="text-mut">Sleep after</span>
        <span className="flex flex-wrap items-center gap-3">
          <select
            aria-label="Sleep after"
            value={sleep || 30}
            disabled={never}
            onChange={(e) => setSleep(Number(e.target.value))}
            className="w-fit font-mono text-xs"
          >
            {choices
              .filter((m) => m > 0)
              .map((m) => (
                <option key={m} value={m}>
                  {minutes(m)}
                </option>
              ))}
          </select>
          <Check label="never" checked={never} onChange={setNever} />
          <span className="text-faint text-xs">without requests</span>
        </span>
        <label htmlFor="pv-copy" className="text-mut">
          Shared values copied
        </label>
        <input
          id="pv-copy"
          value={copies}
          placeholder="none, e.g. SENTRY_DSN"
          spellCheck={false}
          onChange={(e) => setCopies(e.target.value)}
          className="field py-1 font-mono text-xs"
        />
      </div>
      <span className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 text-xs">
          {!maxOk ? (
            <span className="text-fail">Open at most is 1 to 20.</span>
          ) : (
            <ErrorText error={set.error} />
          )}
        </span>
        <Button small onClick={onDone}>
          Cancel
        </Button>
        <Act small primary type="submit" disabled={set.isPending} cli={cli}>
          Save
        </Act>
      </span>
    </form>
  );
}
