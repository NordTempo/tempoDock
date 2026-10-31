import { useState } from "react";
import { Dialog } from "../../ui/Dialog";
import { Act, Button, ErrorText } from "../../ui/kit";
import {
  type Site,
  type SiteSettings,
  useEnableHostname,
  useUpdateSite,
} from "./data";
import { fields, flags, type Key, text, validDays, value } from "./settings";

// A site's settings (ADR 0037, mock C): each one is the install's default
// or the site's own. Only what phase A1 applies is here; replays, errors
// and public dashboards come with their phases.

/** A setting's form value: text, one entry per line for lists. */
type Draft = Record<Key, { custom: boolean; text: string }>;

export default function SiteSettingsDialog({
  project,
  site,
  onRenamed,
  onClose,
}: {
  project: string;
  site: Site;
  onRenamed: (name: string) => void;
  onClose: () => void;
}) {
  const update = useUpdateSite();
  const [name, setName] = useState(site.name);
  const [draft, setDraft] = useState<Draft>(() => {
    const d = {} as Draft;
    for (const { key } of fields) {
      d[key] = {
        custom: site.overrides[key] !== undefined,
        text: text(site.settings, key),
      };
    }
    return d;
  });
  const settings: SiteSettings = {};
  const reset: Key[] = [];
  const cli: string[] = [];
  for (const { key } of fields) {
    const d = draft[key];
    const was = site.overrides[key] !== undefined;
    if (d.custom && (!was || d.text !== text(site.settings, key))) {
      Object.assign(settings, value(key, d.text));
      cli.push(...flags(key, d.text));
    } else if (!d.custom && was) {
      reset.push(key);
      cli.push(`--reset ${key}`);
    }
  }
  const renamed = name.trim() !== site.name && name.trim() !== "";
  if (renamed) cli.unshift(`--name ${name.trim()}`);
  const changed =
    renamed || reset.length > 0 || Object.keys(settings).length > 0;
  const valid =
    !draft.event_retention_days.custom ||
    validDays(draft.event_retention_days.text);

  return (
    <Dialog title={`Site ${site.name}`} onClose={onClose} wide>
      <form
        className="flex flex-col gap-5"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate(
            {
              project,
              site: site.name,
              change: {
                ...(renamed && { name: name.trim() }),
                ...(Object.keys(settings).length && { settings }),
                ...(reset.length && { reset }),
              },
            },
            {
              onSuccess: () => {
                if (renamed) onRenamed(name.trim());
                onClose();
              },
            },
          );
        }}
      >
        <label className="flex flex-col gap-1">
          <span className="text-xs text-mut">Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        {fields.map(({ key, label, help, list }) => {
          const d = draft[key];
          const set = (change: Partial<Draft[Key]>) =>
            setDraft((x) => ({ ...x, [key]: { ...x[key], ...change } }));
          const fallback =
            site.overrides[key] === undefined ? site.settings : undefined;
          return (
            <fieldset key={key} className="flex flex-col gap-1.5">
              <legend className="mb-1 text-sm font-semibold">{label}</legend>
              <span className="seg self-start">
                <button
                  type="button"
                  aria-pressed={!d.custom}
                  onClick={() =>
                    set({
                      custom: false,
                      text: fallback ? text(fallback, key) : d.text,
                    })
                  }
                >
                  Default{fallback ? ` (${summary(fallback, key)})` : ""}
                </button>
                <button
                  type="button"
                  aria-pressed={d.custom}
                  onClick={() => set({ custom: true })}
                >
                  Custom
                </button>
              </span>
              {d.custom &&
                (list ? (
                  <textarea
                    rows={3}
                    className="font-mono"
                    aria-label={label}
                    value={d.text}
                    onChange={(e) => set({ text: e.target.value })}
                  />
                ) : (
                  <input
                    type="number"
                    min={1}
                    max={3650}
                    aria-label={label}
                    className="w-28"
                    value={d.text}
                    onChange={(e) => set({ text: e.target.value })}
                  />
                ))}
              <span className="text-xs text-faint">{help}</span>
            </fieldset>
          );
        })}
        <ErrorText error={update.error} />
        <div className="flex items-center justify-end gap-2.5">
          <Button onClick={onClose}>Cancel</Button>
          <Act
            primary
            type="submit"
            cli={`dock analytics site set ${site.name} -p ${project}${cli.length ? ` ${cli.join(" ")}` : ""}`}
            disabled={!changed || !valid || update.isPending}
          >
            {update.isPending ? "Saving…" : "Save"}
          </Act>
        </div>
      </form>
      <JoinHostname site={site} />
    </Dialog>
  );
}

/** A setting's default as the switch shows it: "none", "395 days". */
function summary(s: SiteSettings, key: Key) {
  const v = s[key];
  if (Array.isArray(v)) return v.length ? `${v.length}` : "none";
  return key === "event_retention_days" ? `${v} days` : String(v);
}

/** Counts another hostname of the environment for this site. */
function JoinHostname({ site }: { site: Site }) {
  const enable = useEnableHostname();
  const [hostname, setHostname] = useState("");
  const host = hostname.trim();
  return (
    <form
      className="mt-5 flex flex-col gap-1.5 border-t border-line pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        enable.mutate(
          { hostname: host, site: site.name },
          { onSuccess: () => setHostname("") },
        );
      }}
    >
      <span className="text-sm font-semibold">Hostnames</span>
      <span className="text-xs text-mut">
        {(site.hostnames ?? [])
          .map((h) => `${h.hostname}${h.service ? ` (${h.service})` : ""}`)
          .join(", ")}
      </span>
      <span className="flex items-center gap-2">
        <input
          aria-label="Hostname to count for this site"
          placeholder="another hostname of this environment"
          className="flex-1"
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
        />
        <Act
          type="submit"
          cli={`dock analytics enable ${host || "<hostname>"} --site ${site.name}`}
          disabled={!host || enable.isPending}
        >
          Count for this site
        </Act>
      </span>
      <ErrorText error={enable.error} />
    </form>
  );
}
