"use client";

import { useState } from "react";

// The agent's blast-radius rule from ADR-0015: recompute the matching pods, floor the
// percentage to whole pods, fault at most that many, and reject a cap that floors to zero.
export default function BlastRadiusDemo() {
  const [pods, setPods] = useState(7);
  const [pct, setPct] = useState(30);
  const cap = Math.floor((pods * pct) / 100);
  const rejected = cap === 0;

  return (
    <div className="demo">
      <div className="grid grid-cols-1 gap-4 border-b border-hairline px-4 py-4 sm:grid-cols-2">
        <div>
          <p className="text-[12px] text-fg-faint">
            pods matching <span className="text-fg">app=checkout-service</span>: <span className="text-fg tabular-nums">{pods}</span>
          </p>
          <input type="range" min={1} max={12} value={pods} onChange={(e) => setPods(Number(e.target.value))} className="range mt-3 w-full" aria-label="Matching pods" />
        </div>
        <div>
          <p className="text-[12px] text-fg-faint">
            maxReplicaPercentage: <span className="text-fg tabular-nums">{pct}</span>
          </p>
          <input type="range" min={0} max={100} step={5} value={pct} onChange={(e) => setPct(Number(e.target.value))} className="range mt-3 w-full" aria-label="Maximum replica percentage" />
        </div>
      </div>
      <div className="px-4 py-5">
        <div className="flex flex-wrap gap-2" aria-label={`${cap} of ${pods} pods would be faulted`}>
          {Array.from({ length: pods }, (_, i) => (
            <span key={i} className={"pod" + (i < cap ? " pod-faulted" : "")} />
          ))}
        </div>
        <p className="mt-4 text-[13px] leading-relaxed text-fg-muted">
          floor({pods} × {pct}%) = <span className="text-fg tabular-nums">{cap}</span>{" "}
          {rejected ? (
            <span className="text-alarm">
              pods. Rejected at admission: a cap that floors to zero is never rounded up.
            </span>
          ) : (
            <>
              pod{cap === 1 ? "" : "s"} faulted, deterministically, from the agent&apos;s own read of the cluster. The other{" "}
              {pods - cap} keep serving.
            </>
          )}
        </p>
      </div>
    </div>
  );
}
