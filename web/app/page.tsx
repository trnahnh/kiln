import Header from "@/components/Header";
import HeroDraft from "@/components/HeroDraft";
import Sheet from "@/components/Sheet";
import Specimens from "@/components/Specimens";
import HashChainDemo from "@/components/HashChainDemo";
import SchedulerDemo from "@/components/SchedulerDemo";
import BlastRadiusDemo from "@/components/BlastRadiusDemo";
import Guardrails from "@/components/Guardrails";
import Invariants from "@/components/Invariants";
import ValidationTable from "@/components/ValidationTable";
import Footer from "@/components/Footer";
import ReplayIntro from "@/components/ReplayIntro";
import { readouts, source } from "@/content/readouts";

export default function Page() {
  return (
    <>
      <link rel="preload" href="/draft/model.json" as="fetch" crossOrigin="anonymous" />
      <div className="px-3 md:px-6">
        <Header />
        <main>
          <HeroDraft />

          <Sheet
            n={2}
            title="Subsystems"
            drawnBy="Go, Java, Crossplane"
            lede="Pick a block. Each sheet shows the stack it runs on, the problem it had to solve, the custom resource or contract it owns, the decision record that settles it, and the number it produced in the validation week."
          >
            <Specimens readouts={readouts} />
          </Sheet>

          <Sheet
            n={3}
            title="Mechanisms"
            drawnBy="The platform's rules"
            lede="Not animations of the idea. The hash rule, the scoring rule and the blast-radius rule below are the platform's own, running in this page."
          >
            <div className="mt-12 grid grid-cols-1 gap-10 lg:grid-cols-12 lg:gap-8">
              <div className="lg:col-span-7">
                <h3 className="demo-title">Test sheet: break the audit chain</h3>
                <p className="demo-lede">
                  Every row&apos;s hash covers its content and the previous row&apos;s hash. Change a field and the row fails
                  verification. Rewrite its stored hash too, the way a careful intruder would, and the next row fails instead.
                </p>
                <HashChainDemo />
              </div>
              <div className="space-y-10 lg:col-span-5">
                <div>
                  <h3 className="demo-title">Calc sheet: score a placement</h3>
                  <p className="demo-lede">
                    50 × cost + 30 × fragmentation + 20 × preemption, on four nodes carrying the placement contract. A
                    latency-sensitive pod never sees the spot nodes: they are filtered before scoring runs.
                  </p>
                  <SchedulerDemo />
                </div>
                <div>
                  <h3 className="demo-title">Plan: cap the blast radius</h3>
                  <p className="demo-lede">
                    The agent floors the percentage to whole pods from its own read of the cluster, and rejects a cap that
                    floors to zero rather than rounding up. Faulted pods are hatched, as a section cut would be.
                  </p>
                  <BlastRadiusDemo />
                </div>
              </div>
            </div>
          </Sheet>

          <Sheet
            n={4}
            title="Injected failures"
            drawnBy="TestPhase7Validation"
            lede={
              <>
                During the validation week one identity claimed too much storage, one shipped a bad version, and one ran a
                full network partition. Each guardrail fired, and each timing was read from audit rows and cross-checked
                on the cluster in{" "}
                <a href={source.runUrl} className="link">
                  run {source.runId}
                </a>
                .
              </>
            }
          >
            <Guardrails />
          </Sheet>

          <Sheet
            n={5}
            title="Invariants"
            drawnBy="docs/decisions"
            lede="Guarantees that hold structurally, not by convention. Each one is settled by an accepted decision record; records are immutable, and a change means a new record that supersedes it."
          >
            <Invariants bare />
          </Sheet>

          <Sheet
            n={6}
            title="Validation"
            drawnBy="docs/METRICS.md"
            lede="Ten developer identities, one simulated week compressed into twenty minutes, three injected failures. The table is the run's own artifact, verbatim; this page fails its build if the two ever differ."
          >
            <ValidationTable bare />
          </Sheet>
        </main>
        <Footer />
      </div>
      <ReplayIntro />
    </>
  );
}
