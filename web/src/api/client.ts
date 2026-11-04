import createFetchClient, { type Middleware } from "openapi-fetch";
import { confirmIdentity } from "./reauth";
import type { components, paths } from "./schema";

// Typed client for the daemon API, generated from web/openapi.json.
// Spec paths carry the /api/v1 prefix (Huma registers full paths), so the
// base URL is the page origin and calls read api.GET("/api/v1/health").
// The session cookie rides along on every request.
export const api = createFetchClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

// The organization the UI acts in, sent as the `org` parameter of every
// call (ADR 0027): the one in the URL (/$org/...), remembered for pages
// outside any org (the console, your account) and the next visit. Unset
// lets the daemon use the user's only one.
const orgKey = "dock.org";
let org = localStorage.getItem(orgKey) ?? undefined;

/** The organization the UI acts in. */
export const currentOrg = () => org;

/** Acts in the organization named by slug from now on (unset: none). */
export function setOrg(slug: string | undefined) {
  org = slug;
  if (slug) localStorage.setItem(orgKey, slug);
  else localStorage.removeItem(orgKey);
}

/**
 * url with the current organization as its `org` parameter, for what
 * can't send a header (EventSource, plain links).
 */
export function withOrg(url: string) {
  if (!org) return url;
  const u = new URL(url, window.location.origin);
  if (!u.searchParams.has("org")) u.searchParams.set("org", org);
  return u.pathname + u.search;
}

// The org rides in a header: adding the `org` parameter would mean
// rebuilding the request, and a rebuilt request loses its body.
api.use({
  onRequest({ request }) {
    if (org) request.headers.set("Dock-Org", org);
    return undefined;
  },
});

type Problem = components["schemas"]["Error"];

// Step-ups (ADR 0049), in one place for every call: a 403 with code
// reauth_required asks the person to confirm it's them (./reauth.ts) and
// sends the call again. A request's body is spent once sent, so each one
// is cloned on the way out; only JSON and bodiless ones, so an upload is
// never held in memory twice. Cancelling the prompt answers with the
// original 403.
const retries = new Map<string, Request>();
// Calls that pass reauth_required through without a prompt (noPrompt).
const quiet = new Set<string>();

const retryable = (request: Request) =>
  request.body === null ||
  !!request.headers.get("Content-Type")?.includes("json");

api.use({
  onRequest({ request, id }) {
    if (retryable(request)) retries.set(id, request.clone());
    return undefined;
  },
  async onResponse({ response, id, options }) {
    const retry = retries.get(id);
    retries.delete(id);
    const silent = quiet.delete(id);
    if (response.status !== 403 || !retry || silent) return undefined;
    const problem: unknown = await response
      .clone()
      .json()
      .catch(() => null);
    if (!isProblem(problem) || problem.code !== "reauth_required") {
      return undefined;
    }
    if (!(await confirmIdentity())) return undefined;
    // Unbound: window.fetch called as options.fetch(...) throws
    // "Illegal invocation".
    const { fetch } = options;
    return fetch(retry);
  },
  onError({ id }) {
    retries.delete(id);
    quiet.delete(id);
    return undefined;
  },
});

/**
 * Per-call middleware for calls nobody just clicked (refetches of values
 * already on screen): reauth_required comes back as an error instead of
 * a prompt out of nowhere.
 */
export const noPrompt: Middleware = {
  onRequest({ id }) {
    quiet.add(id);
    return undefined;
  },
};

/** A failed API call: the problem document's status, code and message. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

const isProblem = (body: unknown): body is Problem =>
  typeof body === "object" && body !== null && "detail" in body;

function toError(response: Response, body: unknown) {
  if (!isProblem(body)) {
    return new ApiError(response.status, "", response.statusText);
  }
  // Validation errors say what is wrong in `errors`, not in `detail`.
  const details = body.errors?.map((e) => e.message).join("; ");
  return new ApiError(
    body.status,
    body.code,
    details ? `${body.detail}: ${details}` : body.detail,
  );
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Resolves an openapi-fetch call to its body, or throws an ApiError. */
export async function body<T>(request: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await request;
  if (!response.ok || data === undefined) throw toError(response, error);
  return data;
}

/** Like `body` for lists, which the API sends as null when empty. */
export const list = async <T>(request: Promise<Result<T[] | null>>) =>
  (await body(request)) ?? [];

/** Like `body` for calls without a response body (204). */
export async function done(request: Promise<Result<unknown>>) {
  const { error, response } = await request;
  if (!response.ok) throw toError(response, error);
}

export const isUnauthorized = (error: unknown) =>
  error instanceof ApiError && error.status === 401;
