import { Dialog } from "../../ui/Dialog";
import { NewEnvironment } from "./NewEnvironment";
import { NewOrg } from "./NewOrg";
import { NewProject } from "./NewProject";
import { NewService } from "./NewService";
import type { CreateRequest } from "./open";

/**
 * The create dialog for a request (open.tsx loads this lazily): new
 * organization, project, environment or service, in the shared modal.
 */
export default function CreateDialog({
  request,
  onClose,
}: {
  request: CreateRequest;
  onClose: () => void;
}) {
  switch (request.kind) {
    case "org":
      return (
        <Dialog title="New organization" onClose={onClose}>
          <NewOrg onClose={onClose} />
        </Dialog>
      );
    case "project":
      return (
        <Dialog title="New project" onClose={onClose}>
          <NewProject onClose={onClose} />
        </Dialog>
      );
    case "environment":
      return (
        <Dialog
          title={`New environment in ${request.project}`}
          onClose={onClose}
        >
          <NewEnvironment
            project={request.project}
            cloneFrom={request.cloneFrom}
            onClose={onClose}
          />
        </Dialog>
      );
    case "service":
      return (
        <Dialog
          title={`New service in ${request.env.project} ${request.env.environment}`}
          onClose={onClose}
          wide
        >
          <NewService
            env={request.env}
            source={request.source}
            onClose={onClose}
          />
        </Dialog>
      );
  }
}
