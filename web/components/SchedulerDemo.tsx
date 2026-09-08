"use client";

import { useState } from "react";

type WorkloadClass = "latency-sensitive" | "standard" | "batch";

interface Node {
  name: string;
  capacityType: "spot" | "on-demand";
  hourlyCost: number;
  preemptionRisk: number;
  cpuCapacity: number;
  cpuUsed: number;
}

// Four illustrative nodes carrying the placement contract's labels. The scoring rule,
// the weights and the class filter are the plugin's own (docs/SYSTEM_DESIGN.md, section 3).
const NODES: Node[] = [
  { name: "worker-a", capacityType: "on-demand", hourlyCost: 0.096, preemptionRisk: 0, cpuCapacity: 4, cpuUsed: 1.2 },
  { name: "worker-b", capacityType: "spot", hourlyCost: 0.037, preemptionRisk: 0.05, cpuCapacity: 4, cpuUsed: 0.4 },
  { name: "worker-c", capacityType: "on-demand", hourlyCost: 0.17, preemptionRisk: 0, cpuCapacity: 8, cpuUsed: 5.6 },
  { name: "worker-d", capacityType: "spot", hourlyCost: 0.065, preemptionRisk: 0.12, cpuCapacity: 8, cpuUsed: 4.4 },
];

const WEIGHTS = { cost: 50, fragmentation: 30, preemption: 20 };

interface Scored {
  node: Node;
  filtered: string | null;
  cost: number;
  fragmentation: number;
  preemption: number;
  score: number;
}

function score(cls: WorkloadClass, cpu: number): Scored[] {
  const feasible = NODES.filter((n) => n.cpuUsed + cpu <= n.cpuCapacity && !(cls === "latency-sensitive" && n.capacityType === "spot"));
  const costs = feasible.map((n) => n.hourlyCost);
  const min = Math.min(...costs);
  const max = Math.max(...costs);
  return NODES.map((n) => {
    if (n.cpuUsed + cpu > n.cpuCapacity) return { node: n, filtered: "does not fit", cost: 0, fragmentation: 0, preemption: 0, score: 0 };
    if (cls === "latency-sensitive" && n.capacityType === "spot") {
      return { node: n, filtered: "workload-class filter", cost: 0, fragmentation: 0, preemption: 0, score: 0 };
    }
    const cost = max === min ? 1 : (max - n.hourlyCost) / (max - min);
    const fragmentation = (n.cpuUsed + cpu) / n.cpuCapacity;
    const preemption = n.capacityType === "on-demand" ? 1 : 1 - n.preemptionRisk;
    return {
      node: n,
      filtered: null,
      cost,
      fragmentation,
      preemption,
      score: WEIGHTS.cost * cost + WEIGHTS.fragmentation * fragmentation + WEIGHTS.preemption * preemption,
    };
  });
}

export default function SchedulerDemo() {
  const [cls, setCls] = useState<WorkloadClass>("latency-sensitive");
  const [cpu, setCpu] = useState(1);
  const scored = score(cls, cpu);
  const winner = scored.filter((s) => !s.filtered).sort((a, b) => b.score - a.score)[0];

  return (
    <div className="demo">
      <div className="grid grid-cols-1 gap-4 border-b border-hairline px-4 py-4 sm:grid-cols-2">
        <div>
          <p className="text-[12px] text-fg-faint">kiln.platform.internal/workload-class</p>
          <div className="mt-2 flex flex-wrap gap-2">
            {(["latency-sensitive", "standard", "batch"] as WorkloadClass[]).map((c) => (
              <button key={c} type="button" className="tab" aria-pressed={cls === c} onClick={() => setCls(c)}>
                {c}
              </button>
            ))}
          </div>
        </div>
        <div>
          <p className="text-[12px] text-fg-faint">
            cpu request <span className="text-fg tabular-nums">{cpu.toFixed(1)}</span>
          </p>
          <input type="range" min={0.5} max={4} step={0.5} value={cpu} onChange={(e) => setCpu(Number(e.target.value))} className="range mt-3 w-full" aria-label="CPU request" />
        </div>
      </div>
      <ol className="divide-y divide-hairline">
        {scored.map((s) => {
          const won = winner?.node.name === s.node.name;
          return (
            <li key={s.node.name} className={"sched-row" + (s.filtered ? " sched-row-out" : "") + (won ? " sched-row-won" : "")}>
              <div className="sched-node">
                <span className="text-fg">{s.node.name}</span>
                <span className="text-fg-faint">
                  {s.node.capacityType}, ${s.node.hourlyCost.toFixed(3)}/h, {s.node.cpuUsed}/{s.node.cpuCapacity} cpu
                  {s.node.capacityType === "spot" ? `, risk ${s.node.preemptionRisk}` : ""}
                </span>
              </div>
              {s.filtered ? (
                <p className="text-[13px] text-fg-faint">filtered before scoring: {s.filtered}</p>
              ) : (
                <div className="sched-terms">
                  <Term label="cost" weight={WEIGHTS.cost} value={s.cost} />
                  <Term label="fragmentation" weight={WEIGHTS.fragmentation} value={s.fragmentation} />
                  <Term label="preemption" weight={WEIGHTS.preemption} value={s.preemption} />
                </div>
              )}
              <p className={"sched-score tabular-nums" + (won ? " text-facet-highlight" : "")}>{s.filtered ? "" : s.score.toFixed(1)}</p>
            </li>
          );
        })}
      </ol>
      <div className="border-t border-hairline px-4 py-3">
        <p className="text-[12px] text-fg-faint">the SCHEDULE event this placement publishes</p>
        <pre className="code mt-1 whitespace-pre-wrap text-facet-highlight">
          {winner
            ? `{"action":"SCHEDULE","details":{"outcome":"Bound","node":"${winner.node.name}","workloadClass":"${cls}"}}`
            : "no feasible node; the pod stays Pending"}
        </pre>
      </div>
    </div>
  );
}

function Term({ label, weight, value }: { label: string; weight: number; value: number }) {
  return (
    <div className="sched-term">
      <span className="text-fg-faint">
        {weight} × {label}
      </span>
      <span className="sched-bar" aria-hidden="true">
        <span style={{ width: `${Math.round(value * 100)}%` }} />
      </span>
      <span className="tabular-nums text-fg-muted">{value.toFixed(2)}</span>
    </div>
  );
}
