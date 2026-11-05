import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { followProjectRename } from "../../data/projectRename";
import { useProjects } from "../../data/queries";
import type { Project } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  invalid,
  nameProblem,
  useValidation,
} from "../create/form";

/** The explicit project rename action. Its form loads only when opened. */
export default function RenameProject({
  project,
  onClose,
}: {
  project: Project;
  onClose: () => void;
}) {
  const org = useOrg();
  const router = useRouter();
  const client = useQueryClient();
  const projects = useProjects();
  const [name, setName] = useState(project.name);
  const rename = useAction(
    async (name: string) => {
      const updated = await body(
        api.PATCH("/api/v1/projects/{project}", {
          params: { path: { project: project.name } },
          body: { name, id: project.id },
        }),
      );
      await followProjectRename(client, router, org, {
        previous_name: project.name,
        project: updated,
      });
      return updated;
    },
    () => [], // followProjectRename moves the affected cache trees.
  );
  const v = useValidation();
  const problem = nameProblem(
    "project",
    name,
    projects.filter((p) => p.id !== project.id).map((p) => p.name),
  );
  const same = name === project.name;
  const nameError =
    problem ??
    v.required(!name, "Enter the new name.") ??
    v.required(same, "Type a name different from the current one.");
  return (
    <Dialog title="Rename project" onClose={onClose}>
      <form
        ref={v.form}
        aria-label="Rename project"
        onSubmit={(event) => {
          event.preventDefault();
          if (rename.isPending || !v.check(!!name && !problem && !same)) {
            return;
          }
          rename.mutate(name, { onSuccess: onClose });
        }}
      >
        <Fields>
          <Field label="Name" error={nameError}>
            <input
              ref={autofocus}
              value={name}
              onChange={(event) => setName(event.target.value)}
              spellCheck={false}
              autoComplete="off"
              aria-invalid={!!nameError}
              className={`${input} ${invalid(nameError)}`}
              disabled={rename.isPending}
            />
          </Field>
        </Fields>
        <p className="mt-3 break-all font-mono text-xs text-sec">
          /{org}/{name || "<name>"}
        </p>
        <p className="mt-2 text-xs text-sec">
          Old project links and scripts will need updating. Running services and
          domains stay unchanged.
        </p>
        <Footer
          submit="Rename project"
          pending={rename.isPending}
          problem={v.problem([!name && "a name"], !!problem || same)}
          error={rename.error}
          cli={[`dock project rename ${project.name} ${name || "<name>"}`]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
