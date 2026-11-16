import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useRef, useState } from "react";
import { useProjects } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { FromTemplate } from "../templates/FromTemplate";
import {
  autofocus,
  Choice,
  Field,
  Fields,
  Footer,
  Group,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "./form";
import { useCreateProject } from "./mutations";

// The Compose converter's form and plan load only when From Compose is
// picked.
const ComposeImport = lazy(() => import("../apply/ComposeImport"));

const starts = [
  ["empty", "Empty"],
  ["template", "From template"],
  ["compose", "From Compose"],
] as const;

/**
 * New project, in its own dialog: `dock project create` (a name, and the
 * project starts with a production environment), the same and then
 * `dock template use` in its production environment, or `dock import
 * compose` from a docker-compose.yml. Opens the new project (a template:
 * its main service).
 */
export function NewProject({ onClose }: { onClose: () => void }) {
  const org = useOrg();
  const projects = useProjects();
  const create = useCreateProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [start, setStart] = useState<(typeof starts)[number][0]>("empty");
  const v = useValidation();
  const problem = nameProblem(
    "project",
    name,
    projects.map((p) => p.name),
  );
  const nameError = problem ?? v.required(!name, "Enter a name, like my-app.");
  // A template's project is created once, even if using the template fails
  // and is tried again.
  const created = useRef("");
  const fields = (
    <Fields>
      <Field
        label="Name"
        error={nameError}
        note={
          start === "empty"
            ? "Starts with a production environment."
            : undefined
        }
      >
        <input
          ref={autofocus}
          value={name}
          onChange={(e) => setName(toName(e.target.value))}
          placeholder="my-app"
          spellCheck={false}
          autoComplete="off"
          aria-invalid={!!nameError}
          className={`${input} ${invalid(nameError)}`}
        />
      </Field>
      <Group label="Start">
        <Choice
          label="Start"
          name="project-start"
          value={start}
          options={starts}
          onChange={setStart}
        />
      </Group>
    </Fields>
  );
  if (start === "template") {
    return (
      <Dialog
        title="New project"
        onClose={onClose}
        className="m-auto max-w-[900px]"
      >
        <FromTemplate
          env={{ project: name, environment: "production" }}
          taken={[]}
          head={fields}
          missing={(!name || !!problem) && "a project name"}
          beforeCli={[`dock project create ${name || "<name>"}`]}
          before={async () => {
            if (created.current === name) return;
            await create.mutateAsync(name);
            created.current = name;
          }}
          onClose={onClose}
        />
      </Dialog>
    );
  }
  return (
    <Dialog title="New project" onClose={onClose} wide={start === "compose"}>
      <form
        ref={v.form}
        aria-label="New project"
        onSubmit={(e) => {
          e.preventDefault();
          // From Compose has its own Preview and Create buttons.
          if (start !== "empty") return;
          if (!v.check(!!name && !problem)) return;
          create.mutate(name, {
            onSuccess: async (p) => {
              await navigate({
                to: "/$org/$project",
                params: { org, project: p.name },
              });
              onClose();
            },
          });
        }}
      >
        {fields}
        {start === "empty" ? (
          <Footer
            submit="Create project"
            pending={create.isPending}
            problem={v.problem([!name && "a name"], !!problem)}
            error={create.error}
            cli={[`dock project create ${name || "<name>"}`]}
            onClose={onClose}
          />
        ) : (
          <Suspense fallback={null}>
            <ComposeImport
              project={name}
              ready={!!name && !problem}
              onClose={onClose}
            />
          </Suspense>
        )}
      </form>
    </Dialog>
  );
}
