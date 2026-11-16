import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { withOrg } from "../../api/client";
import { useArchiveProject } from "../../data/mutations";
import { useIsAdmin, useSession } from "../../data/queries";
import type { Project } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { Act, Button, Confirm, SectionHead } from "../../ui/kit";

// A project's General settings (ADR 0059): its name, its config file and
// archiving it. They were on the project page before.

const RenameProject = lazy(() => import("./RenameProject"));

/** The project's name, with Rename for admins of the whole project. */
export function ProjectName({ project }: { project: Project }) {
  const org = useOrg();
  const session = useSession();
  const canRename =
    session?.scope === "admin" &&
    !session.environment &&
    (!session.project || session.project === project.name);
  const [renaming, setRenaming] = useState(false);
  return (
    <section aria-labelledby="name" className="flex flex-col gap-1.5">
      <SectionHead id="name">Name</SectionHead>
      <p className="flex flex-wrap items-center gap-3">
        <span className="font-mono">{project.name}</span>
        {canRename && (
          <Act
            small
            cli={`dock project rename ${project.name} <name>`}
            onClick={() => setRenaming(true)}
          >
            Rename
          </Act>
        )}
      </p>
      <p className="text-xs text-mut">
        Its address is /{org}/{project.name}; a rename moves every URL under it,
        and open pages follow. ID{" "}
        <span className="font-mono">{project.id}</span>, created{" "}
        {ago(project.created_at)}.
      </p>
      {renaming && (
        <Suspense fallback={null}>
          <RenameProject project={project} onClose={() => setRenaming(false)} />
        </Suspense>
      )}
    </section>
  );
}

/**
 * `dock project export`: the project's config as a download, TOML as the
 * CLI prints it or the API's JSON. Variable values are never in it. With
 * onApply, "Apply file" opens `dock apply FILE` (live projects only).
 */
export function Export({
  project,
  onApply,
  onSaveTemplate,
}: {
  project: string;
  onApply?: () => void;
  /** Admins: save an environment's services as the org's template. */
  onSaveTemplate?: () => void;
}) {
  const path = `/api/v1/projects/${encodeURIComponent(project)}/export`;
  return (
    // Buttons, not inline links: downloading and applying are actions.
    <div className="flex flex-wrap items-center gap-2 text-xs text-mut">
      <span className="mr-1">Config as a file</span>
      <a
        href={withOrg(`${path}.toml`)}
        download
        className="btn sm"
        title={`dock project export ${project} > ${project}.dock.toml`}
      >
        Download .toml
      </a>
      <a
        href={withOrg(path)}
        download={`${project}.dock.json`}
        className="btn sm"
        title={`dock project export ${project} --format json`}
      >
        Download .json
      </a>
      {onApply && (
        <button
          type="button"
          className="btn sm"
          title={`dock apply ${project}.dock.toml -p ${project}`}
          onClick={onApply}
        >
          Apply a file…
        </button>
      )}
      {onSaveTemplate && (
        <button
          type="button"
          className="btn sm"
          title={`dock template save <name> -p ${project} -e <environment>`}
          onClick={onSaveTemplate}
        >
          Save as template…
        </button>
      )}
    </div>
  );
}

/** Admin only, behind a confirm: archive the whole project. */
export function ArchiveProject({ project }: { project: string }) {
  const isAdmin = useIsAdmin();
  const archive = useArchiveProject();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  if (!isAdmin) return null;
  return (
    <section className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-line pt-4">
      <span className="min-w-0 flex-1 text-mut">
        Archiving stops every service and removes its routes. Volumes and
        history are kept.
      </span>
      <Button danger onClick={() => setConfirming(true)}>
        Archive {project}
      </Button>
      {confirming && (
        <Confirm
          danger
          action={`Archive ${project}`}
          cli={`dock project archive ${project}`}
          pending={archive.isPending}
          error={archive.error}
          onConfirm={() =>
            archive.mutate(project, { onSuccess: () => navigate({ to: "/" }) })
          }
          onCancel={() => {
            archive.reset();
            setConfirming(false);
          }}
        >
          Every service in <b>{project}</b> stops and its routes are removed.
          Volumes and history are kept.
        </Confirm>
      )}
    </section>
  );
}
