import FlowItem from "./FlowItem";
import { flow } from "@/content/site";
import { readouts, source } from "@/content/readouts";

export default function Flow() {
  return (
    <section id="flow" className="mx-auto w-full max-w-6xl px-6 py-20 md:px-8 md:py-28">
      <div className="md:grid md:grid-cols-12 md:gap-8">
        <h2 className="text-2xl font-light tracking-tight text-fg md:col-span-5">
          Six subsystems, in the order a request meets them.
        </h2>
        <p className="measure mt-3 text-[15px] leading-relaxed text-fg-faint md:col-span-6 md:col-start-7 md:mt-0">
          Every number is from the validation week in{" "}
          <a href={source.runUrl} className="underline decoration-hairline-strong underline-offset-4 hover:text-fg-muted">
            CI run {source.runId}
          </a>
          , read from audit rows and cross-checked against the cluster, never from a status field.
        </p>
      </div>
      <ol className="flow-rail mt-14 space-y-12 pl-8">
        {flow.map((item, i) => (
          <FlowItem
            key={item.id}
            index={i}
            title={item.title}
            problem={item.problem}
            value={readouts[item.id].value}
            detail={readouts[item.id].detail}
          />
        ))}
      </ol>
    </section>
  );
}
