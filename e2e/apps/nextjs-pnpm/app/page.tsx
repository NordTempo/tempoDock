import { format } from "date-fns";
import { Nav } from "@/components/nav";
import { Counter } from "@/components/counter";
import { posts } from "@/lib/posts";

export const dynamic = "force-dynamic";

export default function Home() {
  return (
    <main>
      <Nav active="/" />
      <h1 className="text-2xl p-4">nextjs-pnpm v1 rendered at {new Date().toISOString()}</h1>
      <Counter />
      <ul className="p-4">
        {posts.map((p) => (
          <li key={p.slug}>{format(p.date, "yyyy-MM-dd")} {p.title}</li>
        ))}
      </ul>
    </main>
  );
}
