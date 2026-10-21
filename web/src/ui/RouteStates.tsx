import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <div className="p-5">
      <p>Not found.</p>
      <Link to="/" className="text-dim hover:text-white">
        Go to overview
      </Link>
    </div>
  );
}

export function RouteError({ error }: { error: unknown }) {
  return (
    <p className="p-5 text-bad">
      {error instanceof Error ? error.message : "Something went wrong"}
    </p>
  );
}
