import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { useProjects } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import {
  autofocus,
  Choice,
  Field,
  Fields,
  Footer,
  Group,
  input,
  nameProblem,
  toName,
} from "./form";
import { useCreateProject } from "./mutations";

// The Compose converter's form and plan load only when From Compose is
// picked.
const ComposeImport = lazy(() => import("../apply/ComposeImport"));

const starts = [
  ["empty", "Empty"],
  ["compose", "From Compose"],
] as const;

/**
 * New project, in its own dialog: `dock project create` (a name, and the
 * project starts with a production environment), or `dock import compose`
 * from a docker-compose.yml. Opens the new project.
 */
export function NewProject({ onClose }: { onClose: () => void }) {
  const org = useOrg();
  const projects = useProjects();
  const create = useCreateProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [start, setStart] = useState<(typeof starts)[number][0]>("empty");
  const problem = nameProblem(
    "project",
    name,
    projects.map((p) => p.name),
  );
  return (
    <Dialog title="New project" onClose={onClose} wide={start === "compose"}>
      <form
        aria-label="New project"
        onSubmit={(e) => {
          e.preventDefault();
          // From Compose has its own Preview and Create buttons.
          if (start !== "empty") return;
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
        <Fields>
          <Field
            label="Name"
            note={
              problem ? (
                <span className="text-fail">{problem}</span>
              ) : start === "empty" ? (
                "Starts with a production environment."
              ) : undefined
            }
          >
            <input
              ref={autofocus}
              value={name}
              onChange={(e) => setName(toName(e.target.value))}
              placeholder="my-app"
              spellCheck={false}
              autoComplete="off"
              className={input}
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
        {start === "empty" ? (
          <Footer
            submit="Create project"
            pending={create.isPending}
            disabled={!name || !!problem}
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
