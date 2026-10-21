// A server-rendered page plus a few static ones, so `next build` does real work.
export const dynamic = "force-dynamic";

export default function Home() {
  return <main>nextjs v1 rendered at {new Date().toISOString()}</main>;
}
