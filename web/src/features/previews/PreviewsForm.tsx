import { type FormEvent, useState } from "react";
import { useProjectOverview } from "../../data/queries";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import {
  PREVIEW_DEFAULT_MAX,
  PREVIEW_MAX,
  type PreviewSettings,
  previewsCli,
  useSetPreviews,
} from "./data";

// Loaded when an admin turns previews on or edits them.

/** `web.PUBLIC_API_URL` or `shared.SENTRY_DSN`. */
const entryRe = /^[a-z0-9][a-z0-9-]*\.[A-Za-z_][A-Za-z0-9_]*$/;

/** The entries of the copied variables field: one per line or comma. */
const entries = (text: string) =>
  text
    .split(/[\s,]+/)
    .map((e) => e.trim())
    .filter(Boolean);

/**
 * Turns previews on or changes them (PUT /projects/{p}/previews): the
 * base, from the project's regular environments, the most open at once,
 * the variables whose literal values are copied and whether forks count.
 */
export default function PreviewsForm({
  project,
  settings,
  onDone,
}: {
  project: string;
  settings: PreviewSettings;
  onDone: () => void;
}) {
  const bases = useProjectOverview(project)
    .environments.filter((e) => !e.environment.preview)
    .map((e) => e.environment);
  const save = useSetPreviews();
  const [base, setBase] = useState(
    settings.base ??
      bases.find((e) => e.kind === "non_production")?.name ??
      bases[0]?.name ??
      "",
  );
  const [max, setMax] = useState(
    String(settings.enabled ? settings.max_previews : PREVIEW_DEFAULT_MAX),
  );
  const [copy, setCopy] = useState(settings.copy_variables.join("\n"));
  const [forks, setForks] = useState(settings.allow_forks);

  const maxN = Number(max);
  const maxOk = Number.isInteger(maxN) && maxN >= 1 && maxN <= PREVIEW_MAX;
  const list = entries(copy);
  const bad = list.filter((e) => !entryRe.test(e));
  const error = !maxOk
    ? `Open previews must be 1 to ${PREVIEW_MAX}.`
    : bad.length > 0
      ? `Write ${bad[0]} as service.KEY or shared.KEY.`
      : null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (error || !base) return;
    save.mutate(
      {
        project,
        req: {
          base,
          max_previews: maxN,
          copy_variables: list,
          allow_forks: forks,
        },
      },
      { onSuccess: onDone },
    );
  };

  if (bases.length === 0) {
    return (
      <p className="text-mut">
        Previews are cloned from an environment; create one first.{" "}
        <button type="button" className="lnk" onClick={onDone}>
          Cancel
        </button>
      </p>
    );
  }

  return (
    <form
      aria-label="Previews"
      onSubmit={submit}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
      className="flex flex-col gap-3"
    >
      <dl className="props">
        <dt>
          <label htmlFor="preview-base">Base</label>
        </dt>
        <dd className="flex flex-wrap items-baseline gap-2">
          <select
            id="preview-base"
            value={base}
            onChange={(e) => setBase(e.target.value)}
            className="field py-[3px] font-mono text-xs"
          >
            {bases.map((e) => (
              <option key={e.id} value={e.name}>
                {e.name}
              </option>
            ))}
          </select>
          <span className="text-xs text-mut">cloned for each pull request</span>
        </dd>
        <dt>
          <label htmlFor="preview-max">Open at most</label>
        </dt>
        <dd className="flex flex-wrap items-baseline gap-2">
          <input
            id="preview-max"
            type="number"
            min={1}
            max={PREVIEW_MAX}
            value={max}
            onChange={(e) => setMax(e.target.value)}
            className="field w-16 py-0.5 font-mono text-xs"
          />
          <span className="text-xs text-mut">
            1 to {PREVIEW_MAX}; more pull requests wait for one to close
          </span>
        </dd>
        <dt>
          <label htmlFor="preview-copy">Copied variables</label>
        </dt>
        <dd className="flex flex-col gap-1">
          <textarea
            id="preview-copy"
            rows={Math.min(Math.max(list.length + 1, 2), 8)}
            value={copy}
            placeholder={"web.PUBLIC_API_URL\nshared.SENTRY_DSN"}
            onChange={(e) => setCopy(e.target.value)}
            spellCheck={false}
            className="field max-w-[420px] py-1 font-mono text-xs"
          />
          <span className="text-xs text-mut">
            One per line, as service.KEY or shared.KEY. References and database
            credentials are always set; other values only when listed here.
          </span>
        </dd>
        <dt>Forks</dt>
        <dd className="flex flex-col gap-1">
          <Check
            label="Preview pull requests from forks"
            checked={forks}
            onChange={setForks}
          />
          {forks && (
            <span className="text-xs text-warn-text">
              A fork is anyone on GitHub: their code runs with the copied
              variables.
            </span>
          )}
        </dd>
      </dl>
      <div className="flex flex-wrap items-center gap-2">
        <Act
          small
          primary
          type="submit"
          disabled={save.isPending || !!error || !base}
          cli={previewsCli(`enable ${base}`, project)}
        >
          {settings.enabled ? "Save" : "Turn on previews"}
        </Act>
        <Button small onClick={onDone}>
          Cancel
        </Button>
        {error && <span className="text-xs text-warn">{error}</span>}
        <ErrorText error={save.error} />
      </div>
    </form>
  );
}
