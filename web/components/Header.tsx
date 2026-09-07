import Link from "next/link";
import { site } from "@/content/site";
import { source } from "@/content/readouts";

// The page header is the general arrangement's title block: the intro's own title block
// fades as this one arrives.
export default function Header() {
  return (
    <header className="arrive header-block mx-auto w-full max-w-[80rem]">
      <div className="grid grid-cols-2 md:grid-cols-[1fr_2.2fr_0.9fr_1fr_1.6fr_1.5fr]">
        <div className="tb-cell">
          <span className="tb-k">Project</span>
          <Link href="/" className="tb-v">
            KILN
          </Link>
        </div>
        <div className="tb-cell">
          <span className="tb-k">Title</span>
          <span className="tb-v">General arrangement, request flow</span>
        </div>
        <div className="tb-cell">
          <span className="tb-k">Sheet</span>
          <span className="tb-v">1 OF 6</span>
        </div>
        <div className="tb-cell">
          <span className="tb-k">Rev</span>
          <span className="tb-v">{source.commit}</span>
        </div>
        <div className="tb-cell">
          <span className="tb-k">Checked</span>
          <a href={source.runUrl} className="tb-v accent">
            CI run {source.runId}
          </a>
        </div>
        <nav aria-label="Primary" className="tb-cell col-span-2 flex items-end gap-5 md:col-span-1">
          {site.nav.map((item) => (
            <a key={item.label} href={item.href} className="tb-nav">
              {item.label}
            </a>
          ))}
        </nav>
      </div>
    </header>
  );
}
