import { useState } from "react";

// The connection form's fields, for adding one and for its settings.

export type ConnectionForm = {
  url: string;
  name: string;
  production: boolean;
  ca_pem: string;
};

/** A name from the URL's database: "postgres://u@h/Shop_db" → "shop-db". */
export function suggestName(url: string) {
  const db = /^postgres(?:ql)?:\/\/[^/]+\/([^?#]+)/.exec(url.trim())?.[1];
  if (!db) return "";
  return decodeURIComponent(db)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

/**
 * URL, name, production and a CA for verify-ca and verify-full
 * (collapsed). When editing, `currentUrl` shows the stored URL with
 * Change, which opens an empty URL field (blank keeps the current one);
 * `urlHint` says what a new one does.
 */
export function ConnectionFields({
  form,
  onChange,
  namePlaceholder,
  currentUrl,
  urlHint,
}: {
  form: ConnectionForm;
  onChange: (next: ConnectionForm) => void;
  namePlaceholder?: string;
  currentUrl?: string;
  urlHint?: string;
}) {
  const [changing, setChanging] = useState(false);
  const set = <K extends keyof ConnectionForm>(k: K, v: ConnectionForm[K]) =>
    onChange({ ...form, [k]: v });
  const editUrl = currentUrl === undefined || changing;
  return (
    <>
      {editUrl ? (
        <label className="flex flex-col gap-1.5">
          <span className="flex items-center gap-3 text-xs text-(--g-dim)">
            URL
            {currentUrl !== undefined && (
              <button
                type="button"
                className="g-lnk"
                onClick={() => {
                  setChanging(false);
                  set("url", "");
                }}
              >
                Keep the current one
              </button>
            )}
          </span>
          <input
            className="g-field h-8 font-mono placeholder:font-sans"
            // biome-ignore lint/a11y/noAutofocus: the form's first field, or opened on request
            autoFocus
            autoComplete="off"
            spellCheck={false}
            placeholder="postgres://user:password@host:5432/db"
            value={form.url}
            onChange={(e) => set("url", e.target.value)}
          />
          <span className="text-xs text-(--g-mut)">
            {urlHint ??
              "Its user sets up tempoBase's roles, so it needs CREATEROLE. Only sslmode is allowed as a parameter (prefer by default). Stored encrypted."}
          </span>
        </label>
      ) : (
        <div className="flex flex-col gap-1.5">
          <span className="text-xs text-(--g-dim)">URL</span>
          <p className="flex flex-wrap items-center gap-3">
            <span className="font-mono text-xs">{currentUrl}</span>
            <button
              type="button"
              className="g-lnk"
              onClick={() => setChanging(true)}
            >
              Change
            </button>
          </p>
        </div>
      )}
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-(--g-dim)">Name</span>
        <input
          className="g-field h-8 w-64 font-mono placeholder:font-sans"
          autoComplete="off"
          spellCheck={false}
          pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?"
          title="Lower case letters, digits and dashes"
          placeholder={namePlaceholder}
          value={form.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <span className="text-xs text-(--g-mut)">In addresses: /c/name</span>
      </label>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={form.production}
          onChange={(e) => set("production", e.target.checked)}
        />
        <span>Production</span>
        <span className="text-xs text-(--g-mut)">
          the production rules apply, and commits ask for the name
        </span>
      </label>
      <details open={!!form.ca_pem}>
        <summary className="cursor-pointer text-xs text-(--g-dim)">
          CA certificate for verify-ca or verify-full
        </summary>
        <textarea
          className="g-field mt-2 h-28 w-full font-mono text-xs"
          spellCheck={false}
          placeholder="-----BEGIN CERTIFICATE-----"
          aria-label="CA certificate (PEM)"
          value={form.ca_pem}
          onChange={(e) => set("ca_pem", e.target.value)}
        />
      </details>
    </>
  );
}
