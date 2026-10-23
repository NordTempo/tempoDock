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
// call (ADR 0025). Unset lets the daemon use the user's only one.
let org: string | undefined;

/** Acts in the organization named by slug from now on (unset: none). */
export function setOrg(slug: string | undefined) {
  org = slug;
}

/** url with the current organization as its `org` parameter. */
export function withOrg(url: string) {
  if (!org) return url;
  const u = new URL(url, window.location.origin);
  if (!u.searchParams.has("org")) u.searchParams.set("org", org);
  return u.pathname + u.search;
}

api.use({
  onRequest({ request }) {
    if (!org) return undefined;
    const url = new URL(request.url);
    if (url.searchParams.has("org")) return undefined;
    url.searchParams.set("org", org);
    return new Request(url, request);
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
