import metrics from "@/content/metrics.json";
import { rowKeys } from "@/content/site";

interface Lane {
  title: string;
  detail: string;
  unit: string;
  span: number;
  ticks: number[];
  marks: { at: number; label: string }[];
}

function policyMs(): number {
  const row = metrics.validation.find((r) => r.requestType === rowKeys.policy);
  return Number((row?.p50 ?? "0").replace(/[^\d.]/g, ""));
}

// Three failures injected on purpose during the validation week, drawn to scale from
// the numbers the run wrote. Each bar is the window the guardrail had; the mark is when it fired.
export default function Guardrails() {
  const g = metrics.guardrails;
  const lanes: Lane[] = [
    {
      title: "Oversized claim",
      detail: "500 GB against a 100 GB ceiling. Denied at admission; the claim never reached the cluster.",
      unit: "ms",
      span: 100,
      ticks: [0, 25, 50, 75, 100],
      marks: [{ at: policyMs(), label: `denied at ${policyMs()} ms` }],
    },
    {
      title: "Bad deploy",
      detail: "A version answering 30% of requests with 500. Rolled back on the mesh, measured from the first traffic split.",
      unit: "s",
      span: 60,
      ticks: [0, 15, 30, 45, 60],
      marks: [{ at: g.rollbackSeconds, label: `rolled back at ${g.rollbackSeconds} s` }],
    },
    {
      title: "SLO breach under chaos",
      detail: "A full network partition against a 20% error bound. Aborted, and the iptables rule was gone from the node one second later.",
      unit: "s",
      span: 30,
      ticks: [0, 10, 20, 30],
      marks: [
        { at: g.abortSeconds, label: `aborted at ${g.abortSeconds} s` },
        { at: g.abortSeconds + g.faultGoneAfterAbortSeconds, label: `fault gone at ${g.abortSeconds + g.faultGoneAfterAbortSeconds} s` },
      ],
    },
  ];

  return (
    <ol className="mt-12 divide-y divide-hairline border-y border-hairline">
      {lanes.map((lane) => (
        <li key={lane.title} className="grid grid-cols-1 gap-4 py-6 md:grid-cols-12 md:gap-8">
          <div className="md:col-span-4">
            <h3 className="text-lg font-semibold tracking-tight text-fg">{lane.title}</h3>
            <p className="measure mt-1.5 text-[14px] leading-relaxed text-fg-muted">{lane.detail}</p>
          </div>
          <div className="md:col-span-8">
            <svg viewBox="0 0 600 56" className="h-14 w-full" role="img" aria-label={lane.marks.map((m) => m.label).join(", ")}>
              <line x1="0" y1="28" x2="600" y2="28" stroke="var(--hairline-strong)" strokeWidth="1" />
              {lane.ticks.map((t) => {
                const x = (t / lane.span) * 600;
                return (
                  <g key={t}>
                    <line x1={x} y1="24" x2={x} y2="32" stroke="var(--hairline-strong)" />
                    <text x={x} y="50" textAnchor={t === 0 ? "start" : t === lane.span ? "end" : "middle"} className="svg-tick">
                      {t} {lane.unit}
                    </text>
                  </g>
                );
              })}
              {lane.marks.map((m, i) => {
                const x = (m.at / lane.span) * 600;
                return (
                  <g key={m.label}>
                    <line x1="0" y1="28" x2={x} y2="28" stroke="var(--facet-deep)" strokeWidth="6" />
                    <path d={`M${x} 20 l6 8 -6 8 -6 -8z`} fill={i === 0 ? "var(--facet-highlight)" : "var(--accent)"} />
                    <text
                      x={lane.marks.length > 1 ? (i === 0 ? x - 10 : x + 10) : x}
                      y="12"
                      textAnchor={lane.marks.length > 1 ? (i === 0 ? "end" : "start") : x > 480 ? "end" : x < 120 ? "start" : "middle"}
                      className="svg-label"
                    >
                      {m.label}
                    </text>
                  </g>
                );
              })}
            </svg>
          </div>
        </li>
      ))}
    </ol>
  );
}
