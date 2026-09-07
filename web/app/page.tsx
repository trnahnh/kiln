import Header from "@/components/Header";
import Hero from "@/components/Hero";
import ReadoutBand from "@/components/ReadoutBand";
import Specimens from "@/components/Specimens";
import HashChainDemo from "@/components/HashChainDemo";
import SchedulerDemo from "@/components/SchedulerDemo";
import BlastRadiusDemo from "@/components/BlastRadiusDemo";
import Guardrails from "@/components/Guardrails";
import Invariants from "@/components/Invariants";
import ValidationTable from "@/components/ValidationTable";
import Footer from "@/components/Footer";
import ReplayIntro from "@/components/ReplayIntro";
import { readouts, source, band } from "@/content/readouts";
import { NODE_IDS } from "@/lib/hero/params";

export default function Page() {
  return (
    <>
      <link rel="preload" href="/hero/lattice.json" as="fetch" crossOrigin="anonymous" />
      <Header />
      <main>
        <Hero band={<ReadoutBand cells={NODE_IDS.map((id) => ({ id, value: band[id].value, label: band[id].label }))} />} />

        <section id="stack" className="mx-auto w-full max-w-6xl px-6 py-20 md:px-8 md:py-28">
          <div className="md:grid md:grid-cols-12 md:gap-8">
            <h2 className="section-title md:col-span-5">Six subsystems. The objects they own.</h2>
            <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
              Pick a node. Each one shows the stack it runs on, the problem it had to solve, the custom resource or
              contract it owns, the decision record that settles it, and the number it produced in the validation week.
            </p>
          </div>
          <Specimens readouts={readouts} />
        </section>

        <section id="mechanisms" className="sheet mx-auto w-full max-w-6xl border-y border-hairline px-6 py-20 md:px-8 md:py-28">
          <div className="md:grid md:grid-cols-12 md:gap-8">
            <h2 className="section-title md:col-span-5">Three mechanisms you can operate.</h2>
            <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
              Not animations of the idea. The hash rule, the scoring rule and the blast-radius rule below are the
              platform&apos;s own, running in this page.
            </p>
          </div>
          <div className="mt-12 grid grid-cols-1 gap-10 lg:grid-cols-12 lg:gap-8">
            <div className="lg:col-span-7">
              <h3 className="demo-title">Break the audit chain</h3>
              <p className="demo-lede">
                Every row&apos;s hash covers its content and the previous row&apos;s hash. Change a field and the row fails
                verification. Rewrite its stored hash too, the way a careful intruder would, and the next row fails instead.
              </p>
              <HashChainDemo />
            </div>
            <div className="space-y-10 lg:col-span-5">
              <div>
                <h3 className="demo-title">Score a placement</h3>
                <p className="demo-lede">
                  50 × cost + 30 × fragmentation + 20 × preemption, on four nodes carrying the placement contract. A
                  latency-sensitive pod never sees the spot nodes: they are filtered before scoring runs.
                </p>
                <SchedulerDemo />
              </div>
              <div>
                <h3 className="demo-title">Cap the blast radius</h3>
                <p className="demo-lede">
                  The agent floors the percentage to whole pods from its own read of the cluster, and rejects a cap that
                  floors to zero rather than rounding up.
                </p>
                <BlastRadiusDemo />
              </div>
            </div>
          </div>
        </section>

        <section id="guardrails" className="mx-auto w-full max-w-6xl px-6 py-20 md:px-8 md:py-28">
          <div className="md:grid md:grid-cols-12 md:gap-8">
            <h2 className="section-title md:col-span-5">Three failures, injected on purpose.</h2>
            <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
              During the validation week one identity claimed too much storage, one shipped a bad version, and one ran
              a full network partition. Each guardrail fired, and each timing below was read from audit rows and
              cross-checked on the cluster in{" "}
              <a href={source.runUrl} className="underline decoration-hairline-strong underline-offset-4 hover:text-fg-muted">
                run {source.runId}
              </a>
              .
            </p>
          </div>
          <Guardrails />
        </section>

        <Invariants />
        <ValidationTable />
      </main>
      <Footer />
      <ReplayIntro />
    </>
  );
}
