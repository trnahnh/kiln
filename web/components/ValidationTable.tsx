import { validationRows, source, completeness } from "@/content/readouts";
import { DOCS } from "@/content/site";

export default function ValidationTable({ bare = false }: { bare?: boolean }) {
  if (bare) return <TableBody />;
  return (
    <section id="validation" className="mx-auto w-full max-w-6xl border-t border-hairline px-6 py-20 md:px-8 md:py-28">
      <div className="md:grid md:grid-cols-12 md:gap-8">
        <h2 className="section-title md:col-span-5">The validation table, as the run wrote it.</h2>
        <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
          Ten developer identities, one simulated week compressed into twenty minutes, three injected failures. The
          table is the run&apos;s own artifact, verbatim from{" "}
          <a href={`${DOCS}/METRICS.md`} className="underline decoration-hairline-strong underline-offset-4 hover:text-fg-muted">
            METRICS.md
          </a>
          ; this page fails its build if the two ever differ.
        </p>
      </div>
      <TableBody />
    </section>
  );
}

function TableBody() {
  return (
    <>
      <table className="validation mt-12">
        <thead>
          <tr>
            <th>Request type</th>
            <th>Baseline (status quo)</th>
            <th>p50</th>
            <th>p95</th>
            <th>n</th>
            <th>Error rate</th>
            <th>Guardrail</th>
          </tr>
        </thead>
        <tbody>
          {validationRows.map((r) => (
            <tr key={r.requestType}>
              <td data-label="Request" className="text-fg">
                {r.requestType}
              </td>
              <td data-label="Baseline" className="max-w-[22rem] text-fg-muted">
                {r.baseline}
              </td>
              <td data-label="p50" className="num">
                {r.p50}
              </td>
              <td data-label="p95" className="num">
                {r.p95}
              </td>
              <td data-label="n" className="num">
                {r.n}
              </td>
              <td data-label="Error rate" className="num">
                {r.errorRate}
              </td>
              <td data-label="Guardrail" className="text-fg-muted">
                {r.guardrail ?? ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-6 text-sm text-fg-faint">
        {completeness.auditRows} audit rows in {completeness.wallClock} of wall clock, {completeness.kafkaAcknowledged}{" "}
        events acknowledged by Kafka, {completeness.publishFailures} publish failures, chain intact. Run{" "}
        <a href={source.runUrl} className="underline decoration-hairline underline-offset-4 hover:text-fg-muted">
          {source.runId}
        </a>{" "}
        on commit {source.commit}.
      </p>
    </>
  );
}
