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
      // Its own dialog: From Compose widens it.
      return <NewProject onClose={onClose} />;
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
      // Its own dialog: the title follows the environment picked in it.
      return (
        <NewService
          env={request.env}
          source={request.source}
          onClose={onClose}
        />
      );
  }
}
