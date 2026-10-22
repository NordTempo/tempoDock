import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <div className="flex w-full max-w-[1100px] flex-col gap-2 px-7 py-6">
      <h1 className="text-xl font-semibold">Not found</h1>
      <p className="text-mut">
        Nothing lives at this address. It may have been renamed or archived.
      </p>
      <Link to="/">Go to the overview</Link>
    </div>
  );
}

export function RouteError({ error }: { error: unknown }) {
  return (
    <div className="w-full max-w-[1100px] px-7 py-6">
      <p className="text-fail">
        <span aria-hidden>✕</span>{" "}
        {error instanceof Error ? error.message : "Something went wrong"}
      </p>
    </div>
  );
}
