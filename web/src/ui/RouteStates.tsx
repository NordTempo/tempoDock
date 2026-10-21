import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <main className="mx-auto flex w-full max-w-[1240px] flex-col gap-2 p-5">
      <h1 className="text-xl font-semibold">Not found</h1>
      <p className="text-mut">
        Nothing lives at this address. It may have been renamed or archived.
      </p>
      <Link to="/">Go to the overview</Link>
    </main>
  );
}

export function RouteError({ error }: { error: unknown }) {
  return (
    <main className="mx-auto w-full max-w-[1240px] p-5">
      <p className="text-fail">
        <span aria-hidden>✕</span>{" "}
        {error instanceof Error ? error.message : "Something went wrong"}
      </p>
    </main>
  );
}
