import type { FileRouteTypes } from "../../routeTree.gen";
import type { TopicId } from "./topics";

type RouteId = FileRouteTypes["id"];

/** Every route id and each of its parents' paths: "/_app/$org/images/$id" gives "/_app/$org/images" too. */
type Prefixes<S extends string> = S extends `${infer Head}/${infer Rest}`
  ? Head | `${Head}/${Prefixes<Rest>}`
  : S;

/**
 * The topic ? opens on each page, by route id or a parent of one. The
 * longest entry that is a prefix of the page's route wins, so a page
 * needs an entry only when it differs from its parent; anything unmapped
 * gets Getting around. A renamed route fails the typecheck here.
 */
const helpAt = {
  "/_app/$org": "start",
  "/_app/$org/$project/compare": "deploying",
  "/_app/$org/$project/settings/previews": "previews",
  "/_app/$org/$project/settings/log-drains": "logs",
  "/_app/$org/$project/settings/variables": "variables",
  "/_app/$org/$project/$environment/$service": "services",
  "/_app/$org/$project/$environment/$service/logs": "logs",
  "/_app/$org/$project/$environment/$service/requests": "requests",
  "/_app/$org/$project/$environment/$service/requests/visitors": "analytics",
  "/_app/$org/$project/$environment/$service/errors": "errors",
  "/_app/$org/$project/$environment/$service/metrics": "metrics",
  "/_app/$org/$project/$environment/$service/jobs": "jobs",
  "/_app/$org/$project/$environment/$service/data": "data",
  "/_app/$org/$project/$environment/$service/variables": "variables",
  "/_app/$org/$project/$environment/$service/settings/domains": "domains",
  "/_app/$org/$project/$environment/$service/settings/backups": "backups",
  "/_app/$org/$project/$environment/$service/history": "deploying",
  "/_app/$org/$project/$environment/$service_/releases": "deploying",
  "/_app/$org/errors": "errors",
  "/_app/$org/requests": "requests",
  "/_app/$org/analytics": "analytics",
  "/_app/$org/images": "images",
  "/_app/$org/status-pages": "status-pages",
  "/_app/$org/inbox": "alerts",
  "/_app/$org/alerts": "alerts",
  "/_app/$org/audit": "organizations",
  "/_app/$org/settings": "organizations",
  "/_app/$org/settings/alerts": "alerts",
  "/_app/$org/settings/members": "access",
  "/_app/$org/settings/tokens": "access",
  "/_app/$org/settings/data-access": "data",
  "/_app/$org/settings/variables": "variables",
  "/_app/$org/settings/analytics": "analytics",
  "/_app/$org/settings/cloudflare": "domains",
  "/_app/account": "access",
  "/_app/console": "servers",
} as const satisfies Partial<Record<Prefixes<RouteId>, TopicId>>;

const entries: Partial<Record<string, TopicId>> = helpAt;

/** The topic for the page whose deepest matched route is `routeId`. */
export function topicAt(routeId: string): TopicId {
  let id = routeId.replace(/\/$/, "");
  while (id) {
    const topic = entries[id];
    if (topic) return topic;
    id = id.slice(0, id.lastIndexOf("/"));
  }
  return "start";
}
