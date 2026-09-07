import Link from "next/link";
import { site } from "@/content/site";

export default function Header() {
  return (
    <header className="arrive mx-auto flex w-full max-w-6xl items-baseline justify-between px-6 pt-6 md:px-8">
      <Link href="/" className="text-lg font-semibold tracking-tight text-fg">
        kiln
      </Link>
      <nav aria-label="Primary" className="flex gap-6 text-sm text-fg-muted">
        {site.nav.map((item) => (
          <a key={item.label} href={item.href} className="transition-colors hover:text-fg">
            {item.label}
          </a>
        ))}
      </nav>
    </header>
  );
}
