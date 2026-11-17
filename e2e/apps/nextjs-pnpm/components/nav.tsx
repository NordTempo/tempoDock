import Link from "next/link";
import { Home, Info, Newspaper } from "lucide-react";
import clsx from "clsx";

export function Nav({ active }: { active: string }) {
  const items = [
    { href: "/", label: "Home", Icon: Home },
    { href: "/blog/post-0", label: "Blog", Icon: Newspaper },
    { href: "/about", label: "About", Icon: Info },
  ];
  return (
    <nav className="flex gap-4 p-4 border-b border-white/10">
      {items.map(({ href, label, Icon }) => (
        <Link key={href} href={href} className={clsx("flex items-center gap-1", active === href && "font-bold")}>
          <Icon size={16} /> {label}
        </Link>
      ))}
    </nav>
  );
}
