// Open /tests/palette.html under Vite. No backend: pure functions only.
import { tabsFor } from "../features/service/tabs";
import { type Command, matchCommands } from "./commands";

function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}

// Which tabs a service has decides the palette's "Logs · api" commands.
type Shape = Parameters<typeof tabsFor>[0];
const labels = (view: Shape) => tabsFor(view).map((t) => t.label);
const app = {
  service: { image: "nginx", stateful: false },
  domains: [],
} as unknown as Shape;
const db = {
  service: { data_engine: "postgres", image: "postgres:17", stateful: true },
  domains: [{ hostname: "db.example.com" }],
} as unknown as Shape;
check(
  !labels(app).includes("Data") && !labels(app).includes("Requests"),
  "an app without a domain has no Data or Requests tab",
);
check(
  labels(app).includes("Variables") && labels(app).includes("Jobs"),
  "every service has Variables and Jobs",
);
check(
  labels(db).includes("Data") &&
    labels(db).includes("Backups") &&
    labels(db).includes("Errors"),
  "a database with a domain has Data, Backups and Errors",
);
check(
  tabsFor(app).every((t) => t.to.startsWith("/$org/")),
  "every tab route is under the organization",
);

// Matching: words in the label or the CLI text, either way round.
const command = (
  id: string,
  group: Command["group"],
  label: string,
  cli: string,
): Command => ({ id, group, label, cli, weight: 0, run: () => {} });
const commands = [
  command("logs", "Go to", "Logs · api production", "G L"),
  command("deploy", "Actions", "Deploy api production", "dock deploy -s api"),
  command("members", "Go to", "Members", "dock org members"),
];
const ids = (query: string) =>
  matchCommands(commands, query).map((m) => m.command.id);
check(ids("logs api").join() === "logs", "'logs api' finds the Logs tab");
check(ids("dock deploy").join() === "deploy", "'dock deploy' finds Deploy");
check(ids("members").join() === "members", "'members' finds the page");

document.title = "PASS: palette";
const results = document.getElementById("results");
if (results) results.textContent = "PASS: service tabs and command matching";
console.log(document.title);
