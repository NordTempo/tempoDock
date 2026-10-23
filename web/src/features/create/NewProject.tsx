import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useProjects } from "../../data/queries";
import { useOrg } from "../../shell/location";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  nameProblem,
  toName,
} from "./form";
import { useCreateProject } from "./mutations";

/**
 * `dock project create`: a name, and the project starts with a production
 * environment. Opens the new project.
 */
export function NewProject({ onClose }: { onClose: () => void }) {
  const org = useOrg();
  const projects = useProjects();
  const create = useCreateProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const problem = nameProblem(
    "project",
    name,
    projects.map((p) => p.name),
  );
  return (
    <form
      aria-label="New project"
      onSubmit={(e) => {
        e.preventDefault();
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
            ) : (
              "Starts with a production environment."
            )
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
      </Fields>
      <Footer
        submit="Create project"
        pending={create.isPending}
        disabled={!name || !!problem}
        error={create.error}
        cli={[`dock project create ${name || "<name>"}`]}
        onClose={onClose}
      />
    </form>
  );
}
