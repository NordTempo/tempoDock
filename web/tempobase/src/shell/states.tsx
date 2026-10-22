import { Link } from "@tanstack/react-router";

// What a route shows when it doesn't exist or failed.

export function NotFound() {
  return (
    <main className="flex flex-col gap-2 p-5">
      <h1 className="text-md font-semibold">Not found</h1>
      <p className="text-(--g-mut)">
        Nothing lives at this address. It may have been renamed or removed.
      </p>
      <Link to="/">Go to the connections</Link>
    </main>
  );
}

export function RouteError({ error }: { error: unknown }) {
  return (
    <main className="p-5">
      <p className="text-(--g-fail)">
        {error instanceof Error ? error.message : "Something went wrong"}
      </p>
    </main>
  );
}
