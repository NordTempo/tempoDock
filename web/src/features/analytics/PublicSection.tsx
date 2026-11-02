import { useQuery } from "@tanstack/react-query";
import { Button, CopyButton } from "../../ui/kit";
import { type PublicAddress, type Site, sitePublicQuery } from "./data";

// A site's public dashboard in its settings (ADR 0037, phase A4; Saile's
// pick: Settings A): off, at its Dock address, or at a domain of its own,
// with the address to copy or the DNS record to create and how it stands;
// and whether the page shows errors. Saved with the rest of the form.

export type PublicMode = "off" | "dock" | "domain";

/** What the section edits. */
export type PublicDraft = { mode: PublicMode; domain: string; errors: boolean };

/** A site's public settings as the section starts from. */
export function publicDraft(site: Site): PublicDraft {
  const domain = site.settings.public_domain ?? "";
  return {
    mode: !site.settings.public ? "off" : domain ? "domain" : "dock",
    domain,
    errors: site.settings.public_errors ?? false,
  };
}

/** The settings and CLI flags that make a site's draft so. */
export function publicChange(site: Site, d: PublicDraft) {
  const was = publicDraft(site);
  const settings: {
    public?: boolean;
    public_domain?: string;
    public_errors?: boolean;
  } = {};
  const flags: string[] = [];
  const on = d.mode !== "off";
  if (on !== (was.mode !== "off")) {
    settings.public = on;
    flags.push(`--public ${on ? "on" : "off"}`);
  }
  // Off keeps the domain for the next time it's on.
  const domain =
    d.mode === "domain" ? d.domain.trim() : d.mode === "dock" ? "" : was.domain;
  if (domain !== was.domain) {
    settings.public_domain = domain;
    flags.push(`--public-domain ${domain || "''"}`);
  }
  if (d.errors !== was.errors) {
    settings.public_errors = d.errors;
    flags.push(`--public-errors ${d.errors ? "on" : "off"}`);
  }
  return {
    settings,
    flags,
    valid: d.mode !== "domain" || d.domain.trim() !== "",
  };
}

const modes: [PublicMode, string][] = [
  ["off", "Off"],
  ["dock", "Dock address"],
  ["domain", "Your domain"],
];

export function PublicSection({
  project,
  site,
  draft,
  onChange,
}: {
  project: string;
  site: Site;
  draft: PublicDraft;
  onChange: (d: PublicDraft) => void;
}) {
  const saved = publicDraft(site);
  // Checked when the dialog opens and on request: a DNS lookup and a TLS
  // handshake per address.
  const status = useQuery({
    ...sitePublicQuery(project, site.name),
    enabled: saved.mode !== "off",
  });
  const addresses = status.data?.addresses ?? [];
  const dock = addresses.find((a) => !a.own);
  const own = addresses.find((a) => a.own);
  // What the address will be is known once saved; until then, the mode's.
  const unsaved =
    draft.mode !== saved.mode ||
    (draft.mode === "domain" && draft.domain.trim() !== saved.domain);
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 text-sm font-semibold">Public dashboard</legend>
      <span className="seg self-start">
        {modes.map(([m, label]) => (
          <button
            key={m}
            type="button"
            aria-pressed={draft.mode === m}
            onClick={() => onChange({ ...draft, mode: m })}
          >
            {label}
          </button>
        ))}
      </span>
      {draft.mode === "dock" &&
        (unsaved ? (
          <span className="text-xs text-faint">
            A read-only page at stats-…, under Dock's base domain, once saved.
          </span>
        ) : dock ? (
          <Address address={dock} />
        ) : (
          status.data && (
            <span className="text-xs text-warn-text">
              Dock has no base domain, so the site has no Dock address; use a
              domain of your own.
            </span>
          )
        ))}
      {draft.mode === "domain" && (
        <>
          <input
            aria-label="Domain for the public dashboard"
            placeholder="stats.example.com"
            className="w-72 font-mono"
            value={draft.domain}
            onChange={(e) => onChange({ ...draft, domain: e.target.value })}
          />
          {!unsaved && own && <Address address={own} records />}
        </>
      )}
      {!unsaved && draft.mode !== "off" && (
        <span className="flex items-center gap-2 text-xs text-faint">
          {status.isFetching ? "Checking…" : "Checked when you open this."}
          <Button
            small
            disabled={status.isFetching}
            onClick={() => status.refetch()}
          >
            Check now
          </Button>
        </span>
      )}
      {draft.mode !== "off" && (
        <span className="flex flex-wrap items-center gap-2 text-sm">
          <span className="seg">
            {([true, false] as const).map((v) => (
              <button
                key={String(v)}
                type="button"
                aria-pressed={draft.errors === v}
                onClick={() => onChange({ ...draft, errors: v })}
              >
                {v ? "Show errors" : "Hide errors"}
              </button>
            ))}
          </span>
          <span className="text-xs text-faint">
            messages and visitor counts only, never stack traces
          </span>
        </span>
      )}
      <span className="text-xs text-faint">
        Anyone with the address sees the site's numbers: no services, releases,
        replays or visitors. Turning it off takes it down at once.
      </span>
    </fieldset>
  );
}

/** An address with its status, and the DNS record it needs while it isn't served. */
function Address({
  address: a,
  records,
}: {
  address: PublicAddress;
  records?: boolean;
}) {
  const tone =
    a.status === "active"
      ? "text-ok"
      : a.status === "error"
        ? "text-fail"
        : "text-warn";
  const said =
    a.status === "active"
      ? "● serving"
      : a.status === "issuing_certificate"
        ? "▲ getting a certificate"
        : a.status === "waiting_for_dns"
          ? "▲ waiting for DNS"
          : "■ error";
  return (
    <div className="flex flex-col gap-1.5">
      <span className="flex flex-wrap items-center gap-2 text-sm">
        <a href={a.url} target="_blank" rel="noreferrer" className="font-mono">
          {a.url}
        </a>
        <CopyButton text={a.url} />
        <span className={`text-xs ${tone}`} title={a.status_reason}>
          {said}
        </span>
      </span>
      {records && a.status !== "active" && (a.dns_records ?? []).length > 0 && (
        <table className="text-xs">
          <tbody>
            {(a.dns_records ?? []).map((r, i) => (
              <tr key={`${r.type}${r.value}`}>
                <td className="w-12 text-faint">
                  {i === 0 ? r.type : `or ${r.type}`}
                </td>
                <td className="font-mono">{r.name}</td>
                <td className="font-mono">{r.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {a.status !== "active" && a.status_reason && (
        <span className="text-xs text-faint">{a.status_reason}</span>
      )}
    </div>
  );
}
