import type { Place } from "./places";

/**
 * Dock's in-app help: one short topic per area. A topic is a line on what
 * it is, the things you do there (each with its `dock` command and the
 * screen it lives on), what catches people out, related topics and the
 * guide page with the rest. No prose beyond that: the guide has it.
 *
 * `{service}`, `{env}` and `{project}` in a command are filled in with
 * the page's own when help opens on one. internal/cli's help test runs
 * every command against the real command tree and docs/guide's checks
 * every `guide` link, so a renamed command or heading fails CI.
 * Routes map to topics in where.ts.
 */

/** Every topic, in the help's order. */
export const topicIds = [
  "start",
  "deploying",
  "services",
  "templates",
  "variables",
  "domains",
  "previews",
  "jobs",
  "data",
  "backups",
  "logs",
  "errors",
  "requests",
  "metrics",
  "analytics",
  "status-pages",
  "alerts",
  "images",
  "access",
  "organizations",
  "servers",
  "cli",
  "agents",
] as const;

export type TopicId = (typeof topicIds)[number];

/** Whether a URL's topic is one of ours. */
export const isTopicId = (id: string): id is TopicId =>
  (topicIds as readonly string[]).includes(id);

export const groups = [
  "Start",
  "Ship",
  "Data",
  "Watch",
  "Organization",
  "Automate",
] as const;

/** One thing to do: what, its command, and the screen it is on. */
export type Do = { what: string; cli?: `dock ${string}`; at?: Place };

export type Topic = {
  title: string;
  group: (typeof groups)[number];
  /** One sentence. */
  what: string;
  /** Three to six. */
  do: readonly [Do, Do, Do, ...Do[]];
  /** What catches people out, at most three short lines. */
  watch: readonly string[];
  related: readonly TopicId[];
  /** A docs/guide page, with a heading's anchor: "deploying#rollback". */
  guide: string;
  /** Search words the text doesn't say. */
  words?: string;
  /** Shown to operators only. */
  operator?: true;
};

export const topics: Record<TopicId, Topic> = {
  start: {
    title: "Getting around",
    group: "Start",
    what: "An organization holds projects; a project holds environments; each environment runs services.",
    do: [
      { what: "Sign in from your terminal", cli: "dock login" },
      { what: "Link a directory, so commands need no flags", cli: "dock link" },
      { what: "See what runs and how it is", cli: "dock status --all" },
      {
        what: "Compare two environments, promote one to the other",
        cli: "dock promote staging production",
        at: { p: "compare" },
      },
      {
        what: "Switch organization",
        cli: "dock org use acme",
        at: { o: "overview" },
      },
    ],
    watch: [
      "Every button shows its dock command on hover; ⌘K finds pages and commands.",
      "Saved changes wait for Apply: the service header counts them.",
    ],
    related: ["deploying", "services", "cli"],
    guide: "getting-started",
    words: "project environment service navigation search palette keys",
  },

  deploying: {
    title: "Deploying",
    group: "Ship",
    what: "Every deploy, restart or settings change makes a numbered release; one serves at a time.",
    do: [
      { what: "Deploy this directory's code", cli: "dock deploy" },
      {
        what: "Deploy an image",
        cli: "dock deploy --image ghcr.io/acme/web:1.4 -s {service} -e {env}",
      },
      {
        what: "Deploy on every git push",
        cli: "dock git connect acme/web -s {service} -e {env}",
        at: { s: "settings" },
      },
      {
        what: "Roll back to the release before",
        cli: "dock rollback -s {service} -e {env}",
        at: { s: "history" },
      },
      {
        what: "See why a release failed",
        cli: "dock diagnose -s {service} -e {env}",
        at: { s: "history" },
      },
      {
        what: "Promote staging's images to production",
        cli: "dock promote staging production",
        at: { p: "compare" },
      },
    ],
    watch: [
      "A release gets traffic once its health check answers; a failed one leaves the old one serving.",
      "Services with a volume are down a few seconds per release: one copy can use a volume at a time.",
    ],
    related: ["services", "previews", "logs"],
    guide: "deploying#how-a-release-goes-live",
    words: "release rollback restart build github git push promote cutover",
  },

  services: {
    title: "Services and settings",
    group: "Ship",
    what: "How a service builds and runs: image or source, resources, volumes, health check.",
    do: [
      {
        what: "Create a service from an image",
        cli: "dock service create worker --image ghcr.io/acme/worker -e {env}",
      },
      {
        what: "Change a setting",
        cli: "dock service update {service} --health-check-path /healthz -e {env}",
        at: { s: "settings" },
      },
      {
        what: "See what waits to apply",
        cli: "dock changes -e {env}",
      },
      { what: "Apply it all as new releases", cli: "dock apply -e {env}" },
      {
        what: "Add a volume",
        cli: "dock volume add data /var/lib/app -s {service} -e {env}",
        at: { s: "settings" },
      },
      {
        what: "Stop a service",
        cli: "dock service stop {service} -e {env}",
        at: { s: "overview" },
      },
    ],
    watch: [
      "Saved changes do nothing until Apply; Apply makes one release per service.",
      "A volume makes the service stateful: deploys stop the old copy before the new one starts.",
    ],
    related: ["deploying", "variables", "domains"],
    guide: "deploying#settings-where-they-come-from",
    words:
      "apply staged discard health check port cpu memory volume start stop",
  },

  templates: {
    title: "Templates",
    group: "Ship",
    what: "Apps and databases created in one step: their services, volumes, secrets and a domain.",
    do: [
      { what: "List the templates", cli: "dock template list" },
      { what: "See a template's inputs", cli: "dock template show umami" },
      {
        what: "Create one in this environment",
        cli: "dock template use umami -e {env}",
      },
      {
        what: "Get it as a project file to edit",
        cli: "dock template use umami --file -e {env}",
      },
      {
        what: "Save this environment as the organization's template",
        cli: "dock template save my-stack -e {env}",
      },
    ],
    watch: [
      "The services are ordinary afterwards; a newer template doesn't change them.",
      "Generated secrets are variables: reveal them on the Variables tab.",
    ],
    related: ["services", "variables", "data"],
    guide: "templates",
    words: "catalog app umami n8n grafana forgejo vaultwarden postgres redis",
  },

  variables: {
    title: "Variables",
    group: "Ship",
    what: "Values a service reads as environment variables: its own, shared by the environment, or the organization's.",
    do: [
      {
        what: "Set a variable",
        cli: "dock env set KEY=value -s {service} -e {env}",
        at: { s: "variables" },
      },
      {
        what: "Share one with every service in the environment",
        cli: "dock env set KEY=value --shared -e {env}",
        at: { p: "shared" },
      },
      {
        what: "Reference another service's",
        cli: `dock env set DATABASE_URL='\${{db.DATABASE_URL}}' -s {service} -e {env}`,
      },
      {
        what: "Import a .env file",
        cli: "dock env import .env -s {service} -e {env}",
      },
      {
        what: "Set one for every project",
        cli: "dock org env set SENTRY_DSN=https://example",
        at: { o: "variables" },
      },
    ],
    watch: [
      "A change is saved, not live: Apply puts it out as a new release.",
      "Only admins read values; a deploy token can replace one but never see it.",
      "Rolling back restores the variables of the release you roll back to.",
    ],
    related: ["services", "deploying", "organizations"],
    guide: "variables",
    words: "env environment secret reference shared dotenv",
  },

  domains: {
    title: "Domains",
    group: "Ship",
    what: "Public hostnames for a service: generated ones work at once, your own once DNS points here.",
    do: [
      {
        what: "Add your own domain",
        cli: "dock domain add shop.example.com -s {service} -e {env}",
        at: { s: "domains" },
      },
      {
        what: "Get a generated address",
        cli: "dock domain generate -s {service} -e {env}",
        at: { s: "domains" },
      },
      {
        what: "Check its DNS and certificate",
        cli: "dock domain check shop.example.com -s {service} -e {env}",
      },
      {
        what: "Redirect a hostname",
        cli: "dock domain add www.example.com --redirect-to https://example.com -s {service} -e {env}",
      },
      {
        what: "Ask for a password",
        cli: "dock access password --username team -s {service} -e {env}",
      },
      {
        what: "Only members signed in to Dock",
        cli: "dock access login -s {service} -e {env}",
      },
    ],
    watch: [
      "Until DNS points at the server, Domains shows the record to create.",
      "Certificates come by themselves once DNS resolves.",
      "With Cloudflare connected, --cloudflare creates the record for you.",
    ],
    related: ["services", "status-pages", "organizations"],
    guide: "domains",
    words:
      "dns tls https certificate hostname redirect password access cloudflare",
  },

  previews: {
    title: "Previews",
    group: "Ship",
    what: "An environment per pull request, cloned from the environment of the services that build it; archived on close.",
    do: [
      {
        what: "Turn them on for a service",
        cli: "dock service previews on -s web",
        at: { p: "previews" },
      },
      {
        what: "Copy one of its values into every preview",
        cli: "dock service previews on -s web --copy STRIPE_KEY",
      },
      {
        what: "Start a database from its newest snapshot",
        cli: "dock service previews -s db --seed",
      },
      {
        what: "Limit how many are open, and when they sleep",
        cli: "dock env previews set staging --max 5 --sleep-after 1h",
      },
      { what: "See open previews", cli: "dock env previews" },
      { what: "Close one", cli: "dock env previews close pr-12" },
    ],
    watch: [
      "Off until an environment admin turns them on for a service that deploys from GitHub.",
      "A pull request that changes nothing in a previewing service's directory gets no preview.",
      "Idle previews sleep and wake on the next request.",
    ],
    related: ["deploying", "variables", "organizations"],
    guide: "previews",
    words: "pull request pr branch github sleep",
  },

  jobs: {
    title: "Jobs",
    group: "Ship",
    what: "Commands run once in a service's image, and commands on a schedule.",
    do: [
      {
        what: "Run a command once",
        cli: "dock run -s {service} -e {env} -- npm run migrate",
        at: { s: "jobs" },
      },
      {
        what: "Open a shell",
        cli: "dock run -s {service} -e {env} --tty -- sh",
      },
      {
        what: "Run one every night",
        cli: "dock job set nightly -s {service} -e {env} --schedule '0 3 * * *' -- npm run report",
        at: { s: "jobs" },
      },
      {
        what: "Run a job now",
        cli: "dock job run nightly -s {service} -e {env}",
      },
      {
        what: "See past runs",
        cli: "dock runs -s {service} -e {env}",
        at: { s: "jobs" },
      },
    ],
    watch: [
      "A run uses the live release's image and variables.",
      "Schedules are read in UTC unless the job sets --timezone.",
    ],
    related: ["deploying", "logs", "data"],
    guide: "deploying#scheduled-jobs",
    words: "cron schedule run command shell migrate one-off",
  },

  data: {
    title: "Databases and Data",
    group: "Data",
    what: "Postgres, MySQL and Redis services, browsed, queried and changed in the Data tab.",
    do: [
      {
        what: "Create a database",
        cli: "dock service create db --template postgres -e {env}",
      },
      {
        what: "Connect from your machine",
        cli: "dock connect -s {service} -e {env}",
        at: { s: "overview" },
      },
      {
        what: "Browse tables",
        cli: "dock db tables -s {service} -e {env}",
        at: { s: "data" },
      },
      {
        what: "Run a query",
        cli: "dock db query 'select count(*) from users' -s {service} -e {env}",
        at: { s: "data" },
      },
      {
        what: "Review changes before they commit",
        cli: "dock db changes list -s {service} -e {env}",
      },
      {
        what: "Find slow queries",
        cli: "dock db queries -s {service} -e {env}",
      },
    ],
    watch: [
      "The data access policy decides who may read or change production data.",
      "Sensitive columns stay masked until revealed, and every reveal is audited.",
    ],
    related: ["backups", "access", "jobs"],
    guide: "data",
    words: "postgres mysql redis valkey sql query table rows policy sensitive",
  },

  backups: {
    title: "Backups",
    group: "Data",
    what: "Database dumps and volume snapshots as restore points; restoring makes a new service beside the old one.",
    do: [
      {
        what: "Back up now",
        cli: "dock backup run -s {service} -e {env}",
        at: { s: "backups" },
      },
      {
        what: "List restore points",
        cli: "dock backup list -s {service} -e {env}",
        at: { s: "backups" },
      },
      {
        what: "Restore into a new service",
        cli: "dock backup restore -s {service} -e {env} --snapshot 4f2a --as db-restored",
      },
      {
        what: "Switch its readers to the new one",
        cli: "dock service switch-references {service} --to db-restored -e {env}",
      },
      {
        what: "Change the hour",
        cli: "dock backup schedule -s {service} -e {env} --hour 4",
      },
      {
        what: "Snapshot a volume",
        cli: "dock volume snapshot -s {service} -e {env}",
      },
    ],
    watch: [
      "Backups run daily at 03:00, the control plane's time.",
      "They stay on the server until an operator sets a repository off it.",
    ],
    related: ["data", "services", "servers"],
    guide: "databases#backups",
    words: "restore snapshot restic dump pg_dump schedule",
  },

  logs: {
    title: "Logs",
    group: "Watch",
    what: "A service's output and its build logs, live.",
    do: [
      {
        what: "Follow live",
        cli: "dock logs -s {service} -e {env} --follow",
        at: { s: "logs" },
      },
      {
        what: "Search the last hour",
        cli: "dock logs -s {service} -e {env} --grep timeout --since 1h",
      },
      {
        what: "Read a release's build",
        cli: "dock logs -s {service} -e {env} --build",
      },
      {
        what: "Send every service's logs elsewhere",
        cli: "dock logs drain add loki https://loki.example.com -e {env}",
        at: { p: "drains" },
      },
      { what: "Test a drain", cli: "dock logs drain test loki -e {env}" },
    ],
    watch: [
      "Errors in the output are grouped on the Errors tab.",
      "A drain covers every service in its environment.",
    ],
    related: ["errors", "requests", "deploying"],
    guide: "watching#logs",
    words: "output stdout stderr build drain loki syslog",
  },

  errors: {
    title: "Errors",
    group: "Watch",
    what: "Errors from logs, browsers and 5xx spikes, grouped, with the release they started in.",
    do: [
      {
        what: "See the organization's errors",
        cli: "dock errors",
        at: { o: "errors" },
      },
      {
        what: "One service's",
        cli: "dock errors -p {project} -e {env} -s {service}",
        at: { s: "errors" },
      },
      { what: "Where, how often, since when", cli: "dock errors show 12" },
      { what: "Mark one resolved", cli: "dock errors resolve 12" },
    ],
    watch: [
      "A resolved error that comes back regresses and alerts.",
      "Browser errors need Analytics on the service's domain.",
    ],
    related: ["logs", "alerts", "analytics"],
    guide: "watching#errors",
    words: "exception crash 5xx stack issue regress",
  },

  requests: {
    title: "Requests and traces",
    group: "Watch",
    what: "What reached a service: traffic by route and status, visitors, and traces from apps that send them.",
    do: [
      {
        what: "Traffic by route and status",
        cli: "dock traffic -s {service} -e {env}",
        at: { s: "requests" },
      },
      {
        what: "The slowest requests",
        cli: "dock traces --slowest -s {service} -e {env}",
        at: { s: "traces" },
      },
      {
        what: "Only failed ones",
        cli: "dock traces --errors -s {service} -e {env}",
      },
      {
        what: "One trace, span by span",
        cli: "dock traces show 4bf92f3577b34da6 -s {service} -e {env}",
      },
      { what: "Turn tracing on (operators)", cli: "dock tracing on" },
    ],
    watch: [
      "Only services with a domain have Requests.",
      "Traces need an OpenTelemetry SDK in the app; Dock sets where it sends.",
    ],
    related: ["metrics", "logs", "analytics"],
    guide: "tracing",
    words: "traffic latency slow spans otel opentelemetry http",
  },

  metrics: {
    title: "Metrics",
    group: "Watch",
    what: "CPU and memory, uptime checks, and the metrics your app sends over OpenTelemetry.",
    do: [
      {
        what: "Set a CPU or memory limit",
        cli: "dock service update {service} --cpus 0.5 --memory 512 -e {env}",
        at: { s: "settings" },
      },
      {
        what: "Check a URL every minute",
        cli: "dock uptime add https://shop.example.com/health -s {service} -e {env}",
        at: { s: "metrics" },
      },
      {
        what: "Read your app's metrics",
        cli: "dock metrics -s {service} -e {env}",
        at: { s: "metrics" },
      },
      {
        what: "Pin a chart",
        cli: "dock metrics pin queue.depth --agg max --by queue -s {service} -e {env}",
      },
    ],
    watch: [
      "App metrics need an OpenTelemetry SDK in the app; Dock sets where it sends.",
      "A service keeps 2,000 series: drop attributes like a user ID from a metric.",
    ],
    related: ["requests", "alerts", "status-pages"],
    guide: "tracing#app-metrics",
    words: "cpu memory resources uptime otel opentelemetry chart p99",
  },

  analytics: {
    title: "Analytics",
    group: "Watch",
    what: "Cookieless visits, browser errors, Web Vitals and replays for your sites.",
    do: [
      {
        what: "Count a domain's visits",
        cli: "dock analytics enable shop.example.com",
        at: { o: "analytics" },
      },
      { what: "Visitors and top pages", cli: "dock analytics stats shop" },
      { what: "Browser errors, grouped", cli: "dock analytics errors shop" },
      { what: "Web Vitals", cli: "dock analytics vitals shop" },
      { what: "Watch a visitor's session", cli: "dock analytics replays shop" },
      {
        what: "Hold everything until consent",
        cli: "dock analytics site set shop --consent all",
      },
      {
        what: "Follow a funnel",
        cli: "dock analytics funnels add shop checkout /cart /pay /done",
      },
    ],
    watch: [
      "An operator picks the analytics server first: dock analytics server.",
      "No cookies; a visitor counts once a day. Settings, Privacy says what needs consent.",
      "Replay records a sampled share of page loads, only on sites that turn it on, and waits for dock.consent(true) by default.",
    ],
    related: ["errors", "requests", "status-pages"],
    guide: "watching#analytics",
    words: "visitors pageviews vitals lcp replay funnel goal site",
  },

  "status-pages": {
    title: "Status pages",
    group: "Watch",
    what: "A public page of your services' status and 90 days of uptime.",
    do: [
      {
        what: "Make one",
        cli: "dock status-page create status",
        at: { o: "statusPages" },
      },
      {
        what: "Show a service on it",
        cli: "dock status-page component add status API --service {service}",
      },
      { what: "Publish it", cli: "dock status-page publish status" },
      {
        what: "Open an incident",
        cli: "dock incident open status 'Checkout is slow' --message 'Looking into it'",
      },
      {
        what: "Use your own hostname",
        cli: "dock status-page hostname status status.example.com",
      },
    ],
    watch: [
      "Uptime comes from Dock's uptime checks: a component without one shows no history.",
      "Nothing is public until you publish.",
    ],
    related: ["metrics", "alerts", "domains"],
    guide: "status-pages",
    words: "incident maintenance uptime public subscribe",
  },

  alerts: {
    title: "Alerts and Inbox",
    group: "Watch",
    what: "Crash loops, failed deploys and failing checks, and access requests waiting for an admin, in one list.",
    do: [
      { what: "See open alerts", cli: "dock alerts", at: { o: "inbox" } },
      {
        what: "Send them to Slack",
        cli: "dock alerts channel add team --slack https://hooks.slack.com/services/T0/B0/x",
        at: { o: "alerts" },
      },
      { what: "Test a channel", cli: "dock alerts channel test team" },
      { what: "Resolve one by hand", cli: "dock alerts resolve 31" },
      {
        what: "Access requests waiting",
        cli: "dock request list --status pending",
        at: { o: "inbox" },
      },
      {
        what: "Make token changes wait for an admin",
        cli: "dock env lock production",
      },
    ],
    watch: [
      "Most alerts resolve themselves once the cause goes away.",
      "A lock stops tokens (CI, agents), not people signed in.",
    ],
    related: ["errors", "access", "agents"],
    guide: "watching#alerts",
    words: "notify slack email webhook lock approve request inbox",
  },

  images: {
    title: "Images",
    group: "Watch",
    what: "Known vulnerabilities in every live image, worst first, grouped by fix.",
    do: [
      { what: "List images", cli: "dock images", at: { o: "images" } },
      {
        what: "A service's vulnerabilities",
        cli: "dock vulns -s {service} -e {env}",
      },
      { what: "Only the fixable ones", cli: "dock vulns --fixable" },
      {
        what: "Ignore one, with a reason",
        cli: "dock vulns ignore CVE-2026-1234 --reason 'not reachable'",
      },
      { what: "Alert on critical ones", cli: "dock alerts vulns critical" },
    ],
    watch: [
      "Scans run once no build or deploy is running.",
      "Most fixes are a rebuild on a newer base image.",
    ],
    related: ["deploying", "alerts", "services"],
    guide: "watching#images",
    words: "cve vulnerability scan sbom security",
  },

  access: {
    title: "Access and tokens",
    group: "Organization",
    what: "Groups give members roles in areas like Deploy or Data, on the organization, a project or an environment; tokens act with a role.",
    do: [
      {
        what: "Invite someone",
        cli: "dock org invite ana@example.com --role deploy",
        at: { o: "members" },
      },
      {
        what: "Put someone in a group",
        cli: "dock group add Developers ana@example.com",
        at: { o: "groups" },
      },
      {
        what: "Give a group a role on staging",
        cli: "dock group grant Contractors deploy:edit -p {project} -e staging",
        at: { o: "groups" },
      },
      {
        what: "What someone can do",
        cli: "dock can ana@example.com",
        at: { o: "members" },
      },
      {
        what: "A token for CI",
        cli: "dock token create ci --scope deploy --project {project}",
        at: { o: "tokens" },
      },
      {
        what: "Revoke a token",
        cli: "dock token revoke 7",
        at: { o: "tokens" },
      },
      {
        what: "Two-factor sign-in for you",
        cli: "dock me two-factor",
        at: { account: true },
      },
      { what: "Require it for everyone", cli: "dock org two-factor on" },
      {
        what: "Email invites (operators set the server)",
        cli: "dock email test",
        at: { console: true },
      },
    ],
    watch: [
      "Reading production data is set apart from the role: --data-production.",
      "A locked environment turns token changes into access requests.",
    ],
    related: ["organizations", "agents", "alerts"],
    guide: "organizations#members-and-roles",
    words:
      "member role group grant invite email smtp token scope 2fa two-factor sso permission",
  },

  organizations: {
    title: "Organizations",
    group: "Organization",
    what: "An organization owns projects, members and settings; you can belong to several.",
    do: [
      { what: "Switch", cli: "dock org use acme", at: { o: "overview" } },
      { what: "Create one", cli: "dock org create acme" },
      {
        what: "Connect GitHub",
        cli: "dock github install",
        at: { o: "github" },
      },
      {
        what: "Connect Cloudflare",
        cli: "dock cloudflare add main --token-stdin",
        at: { o: "cloudflare" },
      },
      {
        what: "Pull from a private registry",
        cli: "dock registry login ghcr.io --username acme",
        at: { o: "registries" },
      },
      {
        what: "See who changed what",
        cli: "dock audit",
        at: { o: "activity" },
      },
    ],
    watch: [
      "A renamed organization keeps its old links working.",
      `Organization variables reach every project: reference them as \${{org.KEY}}.`,
    ],
    related: ["access", "variables", "domains"],
    guide: "organizations",
    words: "org switch slug github cloudflare registry activity audit",
  },

  servers: {
    title: "Servers",
    group: "Organization",
    what: "The machines environments run on, where new ones land, and Dock's own updates.",
    do: [
      { what: "List servers", cli: "dock server list", at: { console: true } },
      {
        what: "Add one for production",
        cli: "dock server add web-2 --production",
      },
      {
        what: "Move an environment",
        cli: "dock env move staging --to web-2",
      },
      { what: "Empty a server", cli: "dock server drain web-1" },
      { what: "Update Dock", cli: "dock update apply" },
    ],
    watch: [
      "Reserved memory is what the services on a server may use at most, added up.",
      "A project keeps each kind of environment, production or not, on one server.",
    ],
    related: ["backups", "deploying", "access"],
    guide: "install",
    words: "server node placement drain update operator console",
    operator: true,
  },

  cli: {
    title: "CLI",
    group: "Automate",
    what: "Everything in the UI works from the dock command line.",
    do: [
      { what: "Sign in", cli: "dock login https://dock.example.com" },
      { what: "Link a directory to a service", cli: "dock link" },
      { what: "Output for scripts", cli: "dock status --json" },
      {
        what: "Save a project as a file",
        cli: "dock export {project}",
      },
      { what: "Apply a project file", cli: "dock apply shop.toml --plan" },
      { what: "Switch between Docks", cli: "dock context use home" },
      { what: "Match the CLI to the server", cli: "dock self-update" },
    ],
    watch: [
      "Hover a button to see its command; ⌘K finds commands too.",
      "--json output and exit codes are stable for scripts.",
    ],
    related: ["agents", "start", "deploying"],
    guide: "README#commands-at-a-glance",
    words:
      "terminal command line json context link apply toml version update install",
  },

  agents: {
    title: "Agents (MCP)",
    group: "Automate",
    what: "Let Claude Code, CI and other agents deploy through MCP with a scoped token.",
    do: [
      { what: "Set up an agent over MCP", cli: "dock mcp" },
      {
        what: "Give it a token for one project",
        cli: "dock token create agent --scope deploy --project {project}",
        at: { o: "tokens" },
      },
      {
        what: "Make production wait for you",
        cli: "dock env lock production",
      },
      {
        what: "Approve what it asks",
        cli: "dock request list --status pending",
        at: { o: "inbox" },
      },
    ],
    watch: [
      "Agents read this guide over MCP, as dock://docs/<page>.",
      "A token limited to one project can't see the others.",
    ],
    related: ["access", "cli", "alerts"],
    guide: "agents#mcp",
    words: "mcp claude agent ai bot ci token",
  },
};
