import { Dialog } from "../../ui/Dialog";
import { NewEnvironment } from "./NewEnvironment";
import { NewProject } from "./NewProject";
import { NewService } from "./NewService";
import type { CreateRequest } from "./open";

/**
 * The create dialog for a request (open.tsx loads this lazily): new
 * project, environment or service, in the shared modal.
 */
export default function CreateDialog({
  request,
  onClose,
}: {
  request: CreateRequest;
  onClose: () => void;
}) {
  switch (request.kind) {
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
