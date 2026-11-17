import { Nav } from "@/components/nav";

export default function About() {
  return (
    <main>
      <Nav active="/about" />
      <p className="p-4">About this app.</p>
    </main>
  );
}
