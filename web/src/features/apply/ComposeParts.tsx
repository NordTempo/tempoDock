import type { ReactNode } from "react";
import type {
  ComposeImport,
  ComposeImportRequest,
  ComposeSecret,
} from "../../data/types";
import { Field, Fields, input } from "../create/form";

// The pieces New project's From Compose and Update from Compose share
// (`dock import compose`, ADR 0049): the Compose and .env inputs, and the
// conversion's files, warnings, secrets and builds.

export type EnvFiles = NonNullable<ComposeImportRequest["env_files"]>;

/** A secret's place in the values the user typed. */
export const secretId = (s: ComposeSecret) =>
  `${s.environment}/${s.service}/${s.key}`;

/** The typed values of the secrets a conversion found, for a request. */
export const secretValues = (
  secrets: readonly ComposeSecret[],
  values: Record<string, string>,
) =>
  secrets.flatMap((s) => {
    const value = values[secretId(s)];
    return value
      ? [{ environment: s.environment, service: s.service, key: s.key, value }]
      : [];
  });

/** envFiles with path set to text, or removed when text is empty. */
export function withEnvFile(envFiles: EnvFiles, path: string, text: string) {
  const next = { ...envFiles };
  if (text) next[path] = text;
  else delete next[path];
  return next;
}

/** The Compose file and .env inputs, each with a Load button. */
export function ComposeInputs({
  compose,
  onCompose,
  onFileName,
  envFiles,
  onEnvFiles,
}: {
  compose: string;
  onCompose: (text: string) => void;
  onFileName: (name: string) => void;
  envFiles: EnvFiles;
  onEnvFiles: (files: EnvFiles) => void;
}) {
  const extra = Object.keys(envFiles).filter((p) => p !== ".env");
  const setDotEnv = (text: string) =>
    onEnvFiles(withEnvFile(envFiles, ".env", text));
  return (
    <Fields>
      <Field
        label="Compose"
        note={
          <Load
            onText={onCompose}
            accept=".yml,.yaml,application/yaml,text/yaml"
            onName={onFileName}
          />
        }
      >
        <textarea
          value={compose}
          onChange={(e) => onCompose(e.target.value)}
          placeholder={
            'services:\n  web:\n    image: nginx\n    ports: ["80:80"]'
          }
          rows={8}
          spellCheck={false}
          autoComplete="off"
          className="field w-full resize-y font-mono text-xs"
        />
      </Field>
      <Field
        label=".env"
        note={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Load onText={setDotEnv} />
            {extra.length > 0 && (
              <span className="font-mono">also sent: {extra.join(", ")}</span>
            )}
          </span>
        }
      >
        <textarea
          value={envFiles[".env"] ?? ""}
          onChange={(e) => setDotEnv(e.target.value)}
          placeholder="optional, for ${VAR} in the Compose file"
          rows={2}
          spellCheck={false}
          autoComplete="off"
          className="field w-full resize-y font-mono text-xs"
        />
      </Field>
    </Fields>
  );
}

/**
 * What a conversion says before its plan: files it wants (Add it sends
 * one and converts again), what didn't map exactly, the secrets with an
 * input each, and the services built from source.
 */
export function ConversionNotes({
  shown,
  project,
  environment,
  values,
  onValues,
  onAddFile,
}: {
  shown: ComposeImport;
  project: string;
  /** The environment the services go in, for the deploy command. */
  environment: string;
  values: Record<string, string>;
  onValues: (values: Record<string, string>) => void;
  onAddFile: (path: string, text: string) => void;
}) {
  const secrets = shown.secrets ?? [];
  return (
    <>
      {(shown.missing_files ?? []).length > 0 && (
        <Section title="Files the Compose file reads">
          {(shown.missing_files ?? []).map((path) => (
            <Warn key={path}>
              <span className="font-mono">{path}</span> wasn't sent{" "}
              <Load label="Add it" onText={(text) => onAddFile(path, text)} />
            </Warn>
          ))}
        </Section>
      )}
      {(shown.warnings ?? []).length > 0 && (
        <Section title="Not mapped exactly">
          {(shown.warnings ?? []).map((w, i) => (
            <Warn key={`${w.service}:${w.key}:${i}`}>
              {w.service && (
                <span className="font-mono text-faint">{w.service} </span>
              )}
              {w.message}
            </Warn>
          ))}
        </Section>
      )}
      {secrets.length > 0 && (
        <Section title="Secrets, kept out of the file">
          <div className="grid grid-cols-[auto_minmax(0,1fr)_minmax(0,180px)] items-center gap-x-3 gap-y-1">
            {secrets.map((s) => {
              const id = secretId(s);
              const where = [s.service, s.key].filter(Boolean).join(" ");
              return (
                <label key={id} className="contents">
                  <span className="font-mono">{where}</span>
                  {s.has_value ? (
                    <span className="text-mut">
                      value from the Compose file
                    </span>
                  ) : (
                    <span className="text-warn">needs a value</span>
                  )}
                  <input
                    type="password"
                    value={values[id] ?? ""}
                    onChange={(e) =>
                      onValues({ ...values, [id]: e.target.value })
                    }
                    placeholder={s.has_value ? "replace" : "value"}
                    aria-label={`Value for ${where}`}
                    autoComplete="new-password"
                    className={input}
                  />
                </label>
              );
            })}
          </div>
        </Section>
      )}
      {(shown.builds ?? []).length > 0 && (
        <Section title="Built from source">
          {(shown.builds ?? []).map((b) => (
            <p key={b.service}>
              <span className="font-mono">{b.service}</span> builds from{" "}
              <span className="font-mono">{b.context}</span>: deploy it with{" "}
              <code className="font-mono">
                dock deploy {b.context} -p {project} -e {environment} -s{" "}
                {b.service}
              </code>{" "}
              once it exists.
            </p>
          ))}
        </Section>
      )}
    </>
  );
}

/** A titled block of the preview, in the plan's heading style. */
export function Section({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-1 text-xs">
      <h3 className="font-semibold">{title}</h3>
      {children}
    </section>
  );
}

/** A ▲ line: something to look at before going on. */
export function Warn({ children }: { children: ReactNode }) {
  return (
    <p className="grid grid-cols-[1ch_minmax(0,1fr)] gap-x-3 [overflow-wrap:anywhere]">
      <span className="text-warn">▲</span>
      <span>{children}</span>
    </p>
  );
}

/** A small button that reads a picked file as text. */
export function Load({
  label = "Load a file",
  accept,
  onText,
  onName,
}: {
  label?: string;
  accept?: string;
  onText: (text: string) => void;
  onName?: (name: string) => void;
}) {
  return (
    <label className="btn sm focus-within:ring-1 focus-within:ring-info">
      {label}
      <input
        type="file"
        accept={accept}
        className="sr-only"
        onChange={async (e) => {
          const el = e.currentTarget;
          const picked = el.files?.[0];
          if (!picked) return;
          onName?.(picked.name);
          onText(await picked.text());
          // Picking the same file again (after an edit) loads it again.
          el.value = "";
        }}
      />
    </label>
  );
}
