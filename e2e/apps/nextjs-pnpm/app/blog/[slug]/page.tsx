import { notFound } from "next/navigation";
import { format } from "date-fns";
import { Nav } from "@/components/nav";
import { posts } from "@/lib/posts";

export function generateStaticParams() {
  return posts.map((p) => ({ slug: p.slug }));
}

export default async function PostPage({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const post = posts.find((p) => p.slug === slug);
  if (!post) notFound();
  return (
    <main>
      <Nav active="/blog/post-0" />
      <article className="p-4">
        <h1 className="text-xl">{post.title}</h1>
        <time>{format(post.date, "PPP")}</time>
        <p>{post.body}</p>
      </article>
    </main>
  );
}
