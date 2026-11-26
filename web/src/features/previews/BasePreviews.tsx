import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { canDeploy } from "../../data/queries";
import type { EnvRef, Session } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { httpsHref } from "../../ui/httpsHref";
import { Act, Button, Check, ErrorText, StateText } from "../../ui/kit";
import { autofocus } from "../create/form";
import {
  type EnvironmentPreviews,
  minutes,
  previewQueries,
  previewsCli,
  useSetEnvironmentPreviews,
} from "./data";
import { previewStates } from "./PreviewParts";

// A base environment's previews (ADR 0056, 0077) as a service that
// builds pull requests shows them in its Settings › Source: the numbers
// every preview cloned from the environment shares, and the open ones.

/** Admin of an environment, as core checks it for preview settings. */
export const canAdmin = (s: Session | null, env: EnvRef) =>
  s?.scope === "admin" && canDeploy(s, env);

/**
 * A base's numbers on a line: how many previews may be open, when they
 * sleep, and the shared variables they copy (none by default: a preview
 * gets no secret nobody chose to give it).
 */
export function PreviewLimits({ numbers }: { numbers: EnvironmentPreviews }) {
  return (
    <>
      {numbers.max_previews}
      <span className="text-mut"> open at most · </span>
      {numbers.sleep_after_minutes > 0 ? (
        <>
          <span className="text-mut">sleep after </span>
          {minutes(numbers.sleep_after_minutes)}
        </>
      ) : (
        <span className="text-mut">never sleep</span>
      )}
      <span className="text-mut"> · shared variables copied: </span>
      {numbers.copy_shared.length > 0 ? (
        <span className="font-mono">{numbers.copy_shared.join(", ")}</span>
      ) : (
        <span className="text-mut">none</span>
      )}
    </>
  );
}

/** The open previews cloned from an environment, newest first. */
export function OpenPreviews({ env }: { env: EnvRef }) {
  const org = useOrg();
  const settings = useQuery(previewQueries.settings(env.project));
  if (settings.error) return <ErrorText error={settings.error} />;
  const open = settings.data?.previews.filter(
    (pv) => pv.base === env.environment,
  );
  if (!open) return null;
  if (open.length === 0) return <span className="text-mut">none</span>;
  return (
    <ul>
      {open.map((pv) => (
        <li
          key={pv.environment}
          className="grid grid-cols-[80px_minmax(0,1fr)_90px_100px_minmax(0,1fr)_60px] items-baseline gap-3 border-soft border-b py-1.5 first:pt-0"
        >
          <Link
            to="/$org/$project/$environment"
            params={{ org, project: env.project, environment: pv.environment }}
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
            <span className="font-mono text-info">#{pv.number}</span> {pv.title}
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
  );
}

/** The sleep choices, in minutes; 0 never. */
const sleepChoices = [0, 15, 30, 60, 120, 360, 1440];

/**
 * Edits a base environment's numbers (`dock env previews set`), from a
 * service of it that builds pull requests. They are the environment's,
 * so the form names the other services they apply to.
 */
export function EnvironmentPreviewsForm({
  env,
  numbers,
  others,
  onDone,
}: {
  env: EnvRef;
  numbers: EnvironmentPreviews;
  /** The environment's other services that build pull requests. */
  others: string[];
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
      className="flex max-w-[760px] flex-col gap-2.5"
    >
      {others.length > 0 && (
        <span className="text-mut text-xs">
          Also for {others.join(", ")}: every preview of {env.environment}{" "}
          shares these.
        </span>
      )}
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
          Shared variables copied
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
