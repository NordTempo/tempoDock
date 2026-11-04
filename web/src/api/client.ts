import createFetchClient from "openapi-fetch";
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
    // A proxy's page or an empty body: say what it means, not "Bad Gateway".
    const message =
      response.status >= 500
        ? `Dock's server didn't answer properly (${response.status}).`
        : response.statusText || `The request failed (${response.status}).`;
    return new ApiError(response.status, "", message);
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

/** Status 0: the request never got an answer (offline, Dock down). */
const unreachable = () =>
  new ApiError(
    0,
    "unreachable",
    "Can't reach Dock. The request never arrived.",
  );

/**
 * Awaits a call, turning fetch's "Failed to fetch" TypeError into an
 * ApiError with status 0 and a message people understand.
 */
async function settle<T>(request: Promise<Result<T>>) {
  try {
    return await request;
  } catch (error) {
    if (error instanceof TypeError) throw unreachable();
    throw error;
  }
}

/** Resolves an openapi-fetch call to its body, or throws an ApiError. */
export async function body<T>(request: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await settle(request);
  if (!response.ok || data === undefined) throw toError(response, error);
  return data;
}

/** Like `body` for lists, which the API sends as null when empty. */
export const list = async <T>(request: Promise<Result<T[] | null>>) =>
  (await body(request)) ?? [];

/** Like `body` for calls without a response body (204). */
export async function done(request: Promise<Result<unknown>>) {
  const { error, response } = await settle(request);
  if (!response.ok) throw toError(response, error);
}

export const isUnauthorized = (error: unknown) =>
  error instanceof ApiError && error.status === 401;
