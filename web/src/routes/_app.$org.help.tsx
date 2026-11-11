import { createFileRoute, Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useIsOperator } from "../data/queries";
import { openKeyHelp } from "../shell/KeyHelp";
import { modKey } from "../shell/keys";
import { useOrg } from "../shell/location";
import { Button, Page, PageBar } from "../ui/kit";

// What Dock's words mean, in one place: the page the sidebar's Help opens
// and the "What's this?" links point into (#releases, #apply, ...).
export const Route = createFileRoute("/_app/$org/help")({
  component: Help,
});

/**
 * One concept: a heading with an anchor, a few plain sentences, and links
 * to the screens where it lives.
 */
function Topic({
  id,
  title,
  go,
  children,
}: {
  id: string;
  title: string;
  go?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-h`}
      className="scroll-mt-20 border-t border-line pt-4"
    >
      <h2 id={`${id}-h`} className="mb-1.5 text-md font-semibold">
        {title}
      </h2>
      <div className="flex max-w-[720px] flex-col gap-2 text-sec">
        {children}
        {go && <p className="flex flex-wrap gap-2">{go}</p>}
      </div>
    </section>
  );
}

function Help() {
  const org = useOrg();
  const operator = useIsOperator();
  return (
    <>
      <PageBar
        title="Help"
        actions={
          <Button onClick={openKeyHelp}>
            Keyboard shortcuts <kbd>?</kbd>
          </Button>
        }
      />
      <Page gap="gap-5">
        <Topic
          id="projects"
          title="Projects, environments and services"
          go={
            <Link to="/$org" params={{ org }} className="lnk">
              Projects
            </Link>
          }
        >
          <p>
            A project holds environments, like production and staging. Each
            environment runs services: apps from an image or a repository, and
            databases. Environments are separate copies; Compare shows how two
            differ and promotes one's images to the other.
          </p>
        </Topic>
        <Topic id="releases" title="Releases, rollback and downtime">
          <p>
            Every deploy, settings change or restart makes a release, numbered
            r1, r2 and on. One release serves at a time; the others are replaced
            or failed. A release only gets traffic once its health check
            answers, so a failed one leaves the previous one serving.
          </p>
          <p>
            Roll back makes a new release with an older one's image and
            settings. Services with a volume are down for a few seconds per
            release, since only one copy can use a volume at a time.
          </p>
        </Topic>
        <Topic id="apply" title="Saved changes and Apply">
          <p>
            Variables, settings, domains and volumes you change are saved first
            and change nothing yet. A service's header counts them ("2 to
            apply"); open the count (A) to review them, Ctrl Enter applies.
            Apply puts them live together as one new release per service, and
            Discard drops them.
          </p>
        </Topic>
        <Topic
          id="locks"
          title="Locks and access requests"
          go={
            <Link to="/$org/inbox" params={{ org }} className="lnk">
              Inbox
            </Link>
          }
        >
          <p>
            Locking an environment makes changes by tokens (CI, scripts, agents)
            wait for an admin's approval as access requests. People signed in
            are not stopped. The Inbox lists what waits.
          </p>
        </Topic>
        <Topic id="domains" title="Domains and DNS">
          <p>
            A domain serves once its DNS points at this server; until then
            Domains shows the record to create. Generated addresses work at
            once. Access sets who may open a service's domains: anyone, a
            password, or members signed in to Dock.
          </p>
        </Topic>
        <Topic id="data" title="Volumes, backups and restore points">
          <p>
            Volumes keep files across releases. Backups (database dumps) and
            snapshots (volume copies) are restore points. Restoring a database
            makes a new service beside the old one; Use switches the services
            that read the old one over to it.
          </p>
        </Topic>
        <Topic
          id="access"
          title="Members, roles and tokens"
          go={
            <>
              <Link
                to="/$org/settings/members"
                params={{ org }}
                className="lnk"
              >
                Members
              </Link>
              <Link to="/$org/settings/tokens" params={{ org }} className="lnk">
                Tokens
              </Link>
            </>
          }
        >
          <p>
            Members are people with a role: read, deploy or admin. Tokens act
            for CI, scripts and agents, with a role of their own, and can be
            limited to one project or environment.
          </p>
        </Topic>
        <Topic
          id="images"
          title="Images and vulnerabilities"
          go={
            <Link to="/$org/images" params={{ org }} className="lnk">
              Images
            </Link>
          }
        >
          <p>
            Dock scans the image of every live release for known
            vulnerabilities, once no build or deploy is running. Images lists
            them worst first. An image's page groups what it found by fix:
            rebuild on a newer base image, upgrade the app's dependencies, or no
            fix yet. A service's overview says so when its live image has
            critical or high ones.
          </p>
        </Topic>
        <Topic id="connect" title="Connecting from your machine">
          <p>
            <code className="font-mono">dock connect</code> opens a port on your
            own machine that reaches a service inside its environment, like a
            database that has no public address. It works while the command
            runs, for people allowed to deploy there.
          </p>
        </Topic>
        <Topic
          id="servers"
          title="Servers, placement and reserved memory"
          go={
            operator && (
              <Link to="/console/servers" className="lnk">
                Servers
              </Link>
            )
          }
        >
          <p>
            Servers run environments. Placement decides which server a new
            environment lands on; a server can take production, other
            environments, or both. Reserved memory is what the services there
            may use at most, added up.
          </p>
        </Topic>
        <Topic id="cli" title="The command line">
          <p>
            Everything here works from the{" "}
            <code className="font-mono">dock</code> command line too. Hover a
            button to see its command, or search for one with {modKey.trim()}K.
          </p>
        </Topic>
      </Page>
    </>
  );
}
