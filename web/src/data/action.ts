import {
  partialMatchKey,
  type QueryClient,
  type QueryKey,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { type NoticeTone, notify } from "../ui/notice";

// Every call that changes something goes through useAction (Biome refuses
// useMutation elsewhere), or settling for one that isn't a mutation. It
// names the cache subtrees the call changes, refetched once it ends; live
// events (./live.ts) refetch the same subtrees on other pages. Nothing
// here is Dock-specific: the data panel, shared with tempoBase, uses it.

// The subtrees of calls still waiting for their answer. Every mutation is
// audited and the server publishes the audit event before it answers, so
// that event usually arrives first: live.ts leaves these subtrees to the
// refetch the call makes once it ends (always later than the event)
// instead of fetching them twice.
const awaiting = new Set<readonly QueryKey[]>();

/** True while a call in flight will refetch the query once it ends. */
export function refetchedOnSettle(queryKey: QueryKey) {
  for (const touched of awaiting)
    if (touched.some((t) => partialMatchKey(queryKey, t))) return true;
  return false;
}

/**
 * Runs a call, then refetches the subtrees it touched, leaving them to it
 * while it runs (refetchedOnSettle). useAction's calls go through it; use
 * it directly for one that isn't a mutation, like a streamed query run.
 */
export async function settling<T>(
  queryClient: QueryClient,
  touched: QueryKey[],
  call: () => Promise<T>,
): Promise<T> {
  awaiting.add(touched);
  try {
    return await call();
  } finally {
    awaiting.delete(touched);
    await Promise.all(
      touched.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
    );
  }
}

/**
 * A mutation that refetches what it touches before it resolves: `touches`
 * returns the subtrees (`() => []` for a call that changes nothing
 * cached, like a sign-in step). A call that puts its answer in the cache
 * does so in mutationFn. Side effects for one caller go in mutate's own
 * onSuccess, which runs after the refetch.
 */
export function useAction<Vars, Data>(
  mutationFn: (vars: Vars) => Promise<Data>,
  touches: (vars: Vars) => QueryKey[],
  /**
   * What to tell the person once it worked, as a notice: "Archived web",
   * or with a tone, ["Restarting web", "info"] for work that runs on.
   */
  said?: (vars: Vars, data: Data) => string | [string, NoticeTone],
) {
  const queryClient = useQueryClient();
  return useMutation<Data, Error, Vars>({
    mutationFn: (vars) =>
      settling(queryClient, touches(vars), () => mutationFn(vars)),
    onSuccess:
      said &&
      ((data, vars) => {
        const s = said(vars, data);
        if (typeof s === "string") notify(s);
        else notify(...s);
      }),
  });
}
