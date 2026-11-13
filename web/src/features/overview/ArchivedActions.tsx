import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys, useSession } from "../../data/queries";
import type { Project } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Act, Confirm } from "../../ui/kit";
import { notify } from "../../ui/notice";

// Rename and Delete permanently for an archived project (ADR 0077), on
// its page and in Organization settings. Both need an admin of the whole
// org; the server checks again.

const RenameProject = lazy(() => import("./RenameProject"));

/** Whether the session may rename and delete projects: an unlimited admin. */
function useOrgAdmin() {
  const session = useSession();
  return session?.scope === "admin" && !session.project && !session.environment;
}

/**
 * `dock project delete`: deletes the project and everything in it, its
 * name typed again in the dialog. It touches no cache itself: refetching
 * the project's queries now answers 404 (and retries), so the dialog
 * drops them once it left the project's page.
 */
const useDeleteProject = (project: Project) =>
  useAction(
    () =>
      body(
        api.DELETE("/api/v1/projects/{project}", {
          params: { path: { project: project.name } },
          body: { confirm: project.name, id: project.id },
        }),
      ),
    () => [],
  );

/**
 * Rename and Delete permanently, as small buttons. onDeleted runs once it
 * is gone (leaving its page), then its cached pages are dropped.
 */
export function ArchivedActions({
  project,
  onDeleted,
}: {
  project: Project;
  onDeleted?: () => Promise<void>;
}) {
  const client = useQueryClient();
  const allowed = useOrgAdmin();
  const remove = useDeleteProject(project);
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  if (!allowed) return null;
  const cli = `dock project delete ${project.name} --confirm ${project.name}`;
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Act
        small
        cli={`dock project rename ${project.name} <name>`}
        onClick={() => setRenaming(true)}
      >
        Rename
      </Act>
      <Act small danger cli={cli} onClick={() => setDeleting(true)}>
        Delete permanently
      </Act>
      {renaming && (
        <Suspense fallback={null}>
          <RenameProject project={project} onClose={() => setRenaming(false)} />
        </Suspense>
      )}
      {deleting && (
        <Confirm
          danger
          action={`Delete ${project.name}`}
          title={`Delete ${project.name} permanently?`}
          typed={project.name}
          cli={cli}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: async (done) => {
                setDeleting(false);
                notify(`Deleted ${done.project}`);
                for (const note of done.notes ?? []) notify(note, "info");
                await onDeleted?.();
                client.removeQueries({ queryKey: keys.project(project.name) });
                client.removeQueries({
                  predicate: ({ queryKey: [root, filter] }) =>
                    root === "audit" &&
                    typeof filter === "object" &&
                    filter !== null &&
                    "project" in filter &&
                    filter.project === project.name,
                });
                await client.invalidateQueries({ queryKey: keys.projects });
              },
            })
          }
          onCancel={() => {
            remove.reset();
            setDeleting(false);
          }}
        >
          Its volumes, containers, releases, variables, domains, logs and
          analytics are deleted from its servers and from Dock, and the name{" "}
          <b>{project.name}</b> is free again. Its backups stay in their
          repository. Its activity stays. This can't be undone.
        </Confirm>
      )}
    </span>
  );
}

/** ArchivedActions on the project's own page: once deleted, go to the org. */
export function ArchivedPageActions({ project }: { project: Project }) {
  const org = useOrg();
  const navigate = useNavigate();
  return (
    <ArchivedActions
      project={project}
      onDeleted={() => navigate({ to: "/$org", params: { org } })}
    />
  );
}
