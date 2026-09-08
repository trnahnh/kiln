import { invariants, adrSlugs, adrUrl } from "@/content/site";

export default function Invariants({ bare = false }: { bare?: boolean }) {
  if (bare) return <InvariantsList />;
  return (
    <section id="invariants" className="mx-auto w-full max-w-6xl border-t border-hairline px-6 py-20 md:px-8 md:py-28">
      <div className="md:grid md:grid-cols-12 md:gap-8">
        <h2 className="section-title md:col-span-5">
          Guarantees that hold structurally, not by convention.
        </h2>
        <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
          Each one is settled by an accepted decision record. Records are immutable; a change means a new record that
          supersedes it.
        </p>
      </div>
      <InvariantsList />
    </section>
  );
}

function InvariantsList() {
  return (
    <>
      <ul className="mt-14 grid grid-cols-1 gap-x-8 gap-y-8 md:grid-cols-2 2xl:grid-cols-3">
        {invariants.map((inv) => (
          <li key={inv.adr} className="border-t border-hairline pt-5">
            <p className="measure text-[15px] leading-relaxed text-fg">{inv.text}</p>
            <a
              href={adrUrl(inv.adr, adrSlugs[inv.adr])}
              className="mt-3 inline-block text-sm text-fg-faint underline decoration-hairline underline-offset-4 transition-colors hover:text-fg-muted"
            >
              Decision record {inv.adr}
            </a>
          </li>
        ))}
      </ul>
    </>
  );
}
