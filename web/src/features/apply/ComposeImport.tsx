import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import type { ComposeImportRequest, ComposeSecret } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Act, Button, ErrorText } from "../../ui/kit";
import { Field, Fields, input } from "../create/form";
import { useImportCompose } from "./data";
import { Plan } from "./PlanView";

type EnvFiles = NonNullable<ComposeImportRequest["env_files"]>;

/** A secret's place in the values the user typed. */
const secretId = (s: ComposeSecret) => `${s.environment}/${s.service}/${s.key}`;

/**
 * New project from a Docker Compose file (`dock import compose`, ADR
 * 0047), inside New project's form under its name. Preview converts and
 * plans; Create creates the project, stages the file with the secrets'
 * values and applies it, then opens the project. Loaded when picked.
 */
export default function ComposeImport({
  project,
  ready,
  onClose,
}: {
  /** The project's name as typed; `ready` says whether it is usable. */
  project: string;
  ready: boolean;
  onClose: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const [compose, setCompose] = useState("");
  const [fileName, setFileName] = useState("docker-compose.yml");
  // .env and the env_file files, by the path the Compose file names them.
  const [envFiles, setEnvFiles] = useState<EnvFiles>({});
  // Secrets' values as typed, by secretId; never shown back.
  const [values, setValues] = useState<Record<string, string>>({});
  const preview = useImportCompose();
  const create = useImportCompose();
  const busy = preview.isPending || create.isPending;

  // A preview is shown only for what is in the form now; an edit to the
  // name or the files makes it stale (values aren't part of it).
  const last = preview.variables;
  const shown =
    last?.project === project &&
    last.compose === compose &&
    last.env_files === envFiles
      ? preview.data
      : undefined;
  const secrets = shown?.secrets ?? [];
  const request = (files: EnvFiles) => ({
    project,
    compose,
    env_files: files,
    values: secrets.flatMap((s) => {
      const value = values[secretId(s)];
      return value
        ? [
            {
              environment: s.environment,
              service: s.service,
              key: s.key,
              value,
            },
          ]
        : [];
    }),
  });

  const setEnvFile = (path: string, text: string) => {
    const next = { ...envFiles };
    if (text) next[path] = text;
    else delete next[path];
    setEnvFiles(next);
    return next;
  };

  const failed = (create.data?.result?.applied ?? []).flatMap((a) =>
    (a.failed ?? []).map((f) => `${a.environment}: ${f}`),
  );
  const openProject = async () => {
    await navigate({ to: "/$org/$project", params: { org, project } });
    onClose();
  };
  const cli = `dock import compose ${fileName} -p ${project || "<name>"}`;
  const extra = Object.keys(envFiles).filter((p) => p !== ".env");

  return (
    <div className="mt-2.5 flex flex-col gap-3.5">
      <Fields>
        <Field
          label="Compose"
          note={
            <Load
              onText={setCompose}
              accept=".yml,.yaml,application/yaml,text/yaml"
              onName={setFileName}
            />
          }
        >
          <textarea
            value={compose}
            onChange={(e) => setCompose(e.target.value)}
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
              <Load onText={(text) => setEnvFile(".env", text)} />
              {extra.length > 0 && (
                <span className="font-mono">also sent: {extra.join(", ")}</span>
              )}
            </span>
          }
        >
          <textarea
            value={envFiles[".env"] ?? ""}
            onChange={(e) => setEnvFile(".env", e.target.value)}
            placeholder="optional, for ${VAR} in the Compose file"
            rows={2}
            spellCheck={false}
            autoComplete="off"
            className="field w-full resize-y font-mono text-xs"
          />
        </Field>
      </Fields>
      <div className="flex flex-wrap items-center gap-2">
        <Act
          cli={cli}
          disabled={!ready || !compose.trim() || busy}
          onClick={() => preview.mutate(request(envFiles))}
        >
          Preview
        </Act>
        <Act
          primary
          cli={`${cli} --apply`}
          disabled={!ready || !shown || busy || create.isSuccess}
          onClick={() =>
            create.mutate(
              { ...request(envFiles), apply: true },
              {
                onSuccess: (r) => {
                  const anyFailed = (r.result?.applied ?? []).some(
                    (a) => (a.failed ?? []).length > 0,
                  );
                  if (!anyFailed) void openProject();
                },
              },
            )
          }
        >
          Create project
        </Act>
        <Button onClick={onClose}>
          {create.isSuccess ? "Close" : "Cancel"}
        </Button>
        <ErrorText error={preview.error ?? create.error} />
      </div>
      {failed.length > 0 && (
        <Section title="Created, but some changes stay staged">
          {failed.map((f) => (
            <Warn key={f}>{f}</Warn>
          ))}
          <span>
            <Button small onClick={() => void openProject()}>
              Open {project}
            </Button>
          </span>
        </Section>
      )}
      {shown && (
        <div className="flex max-h-[55vh] flex-col gap-3 overflow-y-auto border-t border-line pt-3">
          {(shown.missing_files ?? []).length > 0 && (
            <Section title="Files the Compose file reads">
              {(shown.missing_files ?? []).map((path) => (
                <Warn key={path}>
                  <span className="font-mono">{path}</span> wasn't sent{" "}
                  <Load
                    label="Add it"
                    onText={(text) =>
                      preview.mutate(request(setEnvFile(path, text)))
                    }
                  />
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
                          setValues({ ...values, [id]: e.target.value })
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
                    dock deploy {b.context} -p {project} -s {b.service}
                  </code>{" "}
                  after creating.
                </p>
              ))}
            </Section>
          )}
          <Plan plan={shown.plan} staged={false} />
          <details>
            <summary className="cursor-pointer text-xs font-semibold">
              Project file
            </summary>
            <a
              href={`data:application/toml;charset=utf-8,${encodeURIComponent(shown.file)}`}
              download={`${project}.dock.toml`}
              className="lnk text-xs"
            >
              Download {project}.dock.toml
            </a>
            <pre className="mt-1.5 max-h-[40vh] overflow-auto font-mono text-2xs leading-[1.6] text-mut">
              {shown.file}
            </pre>
          </details>
        </div>
      )}
    </div>
  );
}

/** A titled block of the preview, in the plan's heading style. */
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-1 text-xs">
      <h3 className="font-semibold">{title}</h3>
      {children}
    </section>
  );
}

/** A ▲ line: something to look at before creating. */
function Warn({ children }: { children: ReactNode }) {
  return (
    <p className="grid grid-cols-[1ch_minmax(0,1fr)] gap-x-3 [overflow-wrap:anywhere]">
      <span className="text-warn">▲</span>
      <span>{children}</span>
    </p>
  );
}

/** A small button that reads a picked file as text. */
function Load({
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
