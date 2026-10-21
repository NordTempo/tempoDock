import createFetchClient from "openapi-fetch";
import createQueryHooks from "openapi-react-query";
import type { paths } from "./schema";

// Typed client for the daemon API, generated from web/openapi.json.
// Spec paths carry the /api/v1 prefix (Huma registers full paths), so the
// base URL is the page origin and calls read fetchClient.GET("/api/v1/health").
// The session cookie rides along on every request.
export const fetchClient = createFetchClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

// TanStack Query bindings over the same client:
//   $api.useQuery("get", "/api/v1/health")
//   queryClient.ensureQueryData($api.queryOptions("get", "/api/v1/health"))
export const $api = createQueryHooks(fetchClient);
