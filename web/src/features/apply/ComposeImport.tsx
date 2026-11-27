import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useOrg } from "../../shell/location";
import { Act, Button, ErrorText } from "../../ui/kit";
import {
  ComposeInputs,
  ConversionNotes,
  type EnvFiles,
  Section,
  secretValues,
  Warn,
  withEnvFile,
} from "./ComposeParts";
import { useImportCompose } from "./data";
import { Plan } from "./PlanView";

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
    values: secretValues(secrets, values),
  });

  const failed = (create.data?.result?.applied ?? []).flatMap((a) =>
    (a.failed ?? []).map((f) => `${a.environment}: ${f}`),
  );
  const openProject = async () => {
    await navigate({ to: "/$org/$project", params: { org, project } });
    onClose();
  };
  const cli = `dock import compose ${fileName} -p ${project || "<name>"}`;

  return (
    <div className="mt-2.5 flex flex-col gap-3.5">
      <ComposeInputs
        compose={compose}
        onCompose={setCompose}
        onFileName={setFileName}
        envFiles={envFiles}
        onEnvFiles={setEnvFiles}
      />
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
          <ConversionNotes
            shown={shown}
            project={project}
            values={values}
            onValues={setValues}
            onAddFile={(path, text) => {
              const files = withEnvFile(envFiles, path, text);
              setEnvFiles(files);
              preview.mutate(request(files));
            }}
          />
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
