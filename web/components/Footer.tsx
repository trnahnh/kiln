import { REPO, DOCS } from "@/content/site";
import { source } from "@/content/readouts";

export default function Footer() {
  return (
    <footer className="mx-auto w-full max-w-6xl border-t border-hairline px-6 py-10 text-sm text-fg-faint md:px-8">
      <div className="flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3">
        <p>
          <a href={REPO} className="text-fg-muted hover:text-fg">
            github.com/trnahnh/kiln
          </a>
          , Apache-2.0.
        </p>
        <p>
          Validated on{" "}
          <a href={source.runUrl} className="underline decoration-hairline underline-offset-4 hover:text-fg-muted">
            run {source.runId}
          </a>
          , commit {source.commit}. Numbers from{" "}
          <a href={`${DOCS}/METRICS.md`} className="underline decoration-hairline underline-offset-4 hover:text-fg-muted">
            METRICS.md
          </a>
          .
        </p>
      </div>
    </footer>
  );
}
