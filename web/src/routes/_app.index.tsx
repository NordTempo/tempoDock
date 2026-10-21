import {
  createFileRoute,
  Link,
  redirect,
  useNavigate,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { queries, useProjects, useServers } from "../data/queries";
import { lastProject } from "../shell/location";
import { CopyButton, Page } from "../ui/kit";

// "/" lands on the last project looked at (or the first); with no projects
// yet, it says how to deploy one.
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const projects = await context.queryClient.ensureQueryData(
      queries.projects(),
    );
    const last = lastProject();
    const project = projects.find((p) => p.name === last) ?? projects[0];
    if (project) {
      throw redirect({ to: "/$project", params: { project: project.name } });
    }
  },
  component: NoProjects,
});

function NoProjects() {
  const server = useServers().find((s) => s.local)?.name;
  const first = useProjects()[0]?.name;
  const navigate = useNavigate();
  // Live events refresh the project list; follow the first one in.
  useEffect(() => {
    if (first) navigate({ to: "/$project", params: { project: first } });
  }, [first, navigate]);
  const steps = [
    [`dock login ${window.location.origin}`, "Sign in from your terminal."],
    [
      "dock project create my-app",
      "Creates the project with a production environment.",
    ],
    [
      "dock link -p my-app",
      "Run it in your app's folder, so later commands need no flags.",
    ],
    [
      "dock deploy -s web",
      "Builds the folder, starts and health checks it. The service is created on first deploy.",
    ],
  ];
  return (
    <Page width="max-w-[720px]" gap="gap-5">
      <h1 className="pt-[52px] text-[24px] font-semibold">No projects yet</h1>
      <p className="text-[15px] text-mut">
        Deploy your first app from its folder.
        {server && (
          <>
            {" "}
            It runs on <span className="font-mono text-white">{server}</span>,
            the server you connected.
          </>
        )}
      </p>
      <ol className="border-t border-line">
        {steps.map(([command = "", note], i) => (
          <li
            key={command}
            className="grid grid-cols-[28px_minmax(0,1fr)_auto] items-baseline gap-x-3.5 gap-y-1 border-b border-sel py-3.5"
          >
            <span className="font-mono text-mut">{i + 1}</span>
            <code className="font-mono">{command}</code>
            <CopyButton text={command} />
            <span />
            <span className="col-span-2 text-mut">{note}</span>
          </li>
        ))}
      </ol>
      <p className="text-mut">
        This page updates when the first project appears. Letting an agent do
        it? <Link to="/tokens">Create a deploy token</Link>.
      </p>
    </Page>
  );
}
