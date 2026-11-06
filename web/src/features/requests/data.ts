import {
  type QueryClient,
  queryOptions,
  useQuery,
} from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import { useAction } from "../../data/mutations";
import { useIsAdmin } from "../../data/queries";
import type { AccessRequest } from "../../data/types";

// Access requests (ADR 0044): calls a lock stopped, waiting for an admin.
// The inbox, the nav badge and the environment strips share one list;
// `access_request` events patch it and the open detail (applyAccessRequestEvent).
// Nothing polls.

/** Requests the inbox loads, newest first; events keep it at this. */
const LIMIT = 100;

export const requestKeys = {
  all: ["requests"],
  list: ["requests", "list"],
  one: (id: number) => ["requests", "one", id],
  check: (id: number) => ["requests", "check", id],
} as const;

export const requestQueries = {
  list: () =>
    queryOptions({
      queryKey: requestKeys.list,
      queryFn: () =>
        list(
          api.GET("/api/v1/requests", { params: { query: { limit: LIMIT } } }),
        ),
    }),
  /** One request; admins get its arguments and the changes since. */
  one: (id: number) =>
    queryOptions({
      queryKey: requestKeys.one(id),
      queryFn: () =>
        body(api.GET("/api/v1/requests/{id}", { params: { path: { id } } })),
    }),
  /**
   * Whether approving would run now, asked of the server (check-request:
   * the call's own checks as the token, stopping at the lock). Asked each
   * time a request is shown (staleTime 0), never on a timer; admins in a
   * UI session only.
   */
  check: (id: number) =>
    queryOptions({
      queryKey: requestKeys.check(id),
      queryFn: () =>
        body(
          api.POST("/api/v1/requests/{id}/check", { params: { path: { id } } }),
        ),
      staleTime: 0,
    }),
};

/**
 * Whether a request still waits for a decision. A pending one past its
 * time reads as expired, like the API shows it once refetched.
 */
export const isPending = (r: AccessRequest, now = Date.now()) =>
  r.status === "pending" && Date.parse(r.expires_at) > now;

/**
 * The requests where admins see them (the nav badge, environment strips);
 * empty for everyone else.
 */
export function useRequestsIfAdmin(): AccessRequest[] {
  const admin = useIsAdmin();
  return useQuery({ ...requestQueries.list(), enabled: admin }).data ?? [];
}

/**
 * A request was filed, decided or finished: insert or replace it in the
 * list and the detail, where cached. Events carry no arguments, so the
 * detail keeps the ones it loaded.
 */
export function applyAccessRequestEvent(
  queryClient: QueryClient,
  request: AccessRequest,
) {
  queryClient.setQueryData<AccessRequest[]>(requestKeys.list, (list) =>
    list
      ? [request, ...list.filter((r) => r.id !== request.id)]
          .sort((a, b) => b.id - a.id)
          .slice(0, LIMIT)
      : list,
  );
  queryClient.setQueryData<AccessRequest>(
    requestKeys.one(request.id),
    (old) =>
      old && {
        ...request,
        args: old.args,
        changes_since: old.changes_since,
      },
  );
}

/**
 * Approves (runs the call as the token that asked) or rejects a request.
 * Approval answers with the outcome, or while it still runs; a later
 * event says how it ended.
 */
export const useDecideRequest = () =>
  useAction(
    ({
      id,
      approve,
      comment,
    }: {
      id: number;
      approve: boolean;
      comment: string;
    }) =>
      body(
        approve
          ? api.POST("/api/v1/requests/{id}/approve", {
              params: { path: { id } },
              body: { comment },
            })
          : api.POST("/api/v1/requests/{id}/reject", {
              params: { path: { id } },
              body: { comment },
            }),
      ),
    () => [requestKeys.all, ["audit"]],
  );
