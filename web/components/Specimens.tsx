"use client";

import { useEffect, useState } from "react";
import CodePanel from "./CodePanel";
import FacetGlyph from "./FacetGlyph";
import { specimens } from "@/content/stack";
import { adrSlugs, adrUrl } from "@/content/site";
import { adrSlugsExtra } from "@/content/stack";
import type { Readout } from "@/content/readouts";
import type { NodeId } from "@/lib/hero/params";

interface Props {
  readouts: Record<NodeId, Readout>;
}

function glow(node: number | null) {
  window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node } }));
}

export default function Specimens({ readouts }: Props) {
  const [active, setActive] = useState(0);
  useEffect(() => {
    const onSelect = (e: Event) => {
      const node = (e as CustomEvent<{ node: number }>).detail?.node;
      if (typeof node === "number" && node >= 0 && node < specimens.length) setActive(node);
    };
    window.addEventListener("select-specimen", onSelect);
    return () => window.removeEventListener("select-specimen", onSelect);
  }, []);
  const s = specimens[active];
  const slug = adrSlugs[s.adr] ?? adrSlugsExtra[s.adr];

  return (
    <div className="mt-12">
      <div role="tablist" aria-label="Subsystems" className="flex flex-wrap gap-2 border-b border-hairline pb-4">
        {specimens.map((sp, i) => (
          <button
            key={sp.id}
            role="tab"
            type="button"
            aria-selected={i === active}
            className="tab"
            onClick={() => setActive(i)}
            onPointerEnter={() => glow(i)}
            onPointerLeave={() => glow(null)}
          >
            <FacetGlyph className="tab-glyph" />
            {sp.id}
          </button>
        ))}
      </div>

      <div role="tabpanel" className="mt-10 grid grid-cols-1 gap-10 lg:grid-cols-12 lg:gap-12">
        <div className="lg:col-span-5 2xl:col-span-4">
          <h3 className="text-2xl font-semibold tracking-tight text-fg">{s.title}</h3>
          <p className="measure mt-3 text-base leading-relaxed text-fg-muted">{s.role}</p>
          <ul className="mt-6 flex flex-wrap gap-2">
            {s.stack.map((chip) => (
              <li key={chip} className="chip">
                {chip}
              </li>
            ))}
          </ul>
          <ul className="mt-8 space-y-4 border-t border-hairline pt-6">
            {s.problems.map((p) => (
              <li key={p} className="measure flex gap-3 text-[15px] leading-relaxed text-fg-muted">
                <span className="mt-2 block h-1.5 w-1.5 flex-none rotate-45 bg-accent" aria-hidden="true" />
                <span>{p}</span>
              </li>
            ))}
          </ul>
          <div className="mt-8 grid grid-cols-1 gap-6 border-t border-hairline pt-6 sm:grid-cols-2">
            <div>
              <p className="text-[13px] text-fg-faint">In the validation week</p>
              <p className="mt-1 text-2xl font-semibold tracking-tight text-fg tabular-nums">{readouts[s.id].value}</p>
              <p className="mt-1 text-[13px] leading-relaxed text-fg-muted">{readouts[s.id].detail}</p>
            </div>
            <div>
              <p className="text-[13px] text-fg-faint">Settled by</p>
              <a
                href={adrUrl(s.adr, slug)}
                className="mt-1 inline-block text-[15px] text-fg underline decoration-hairline-strong underline-offset-4 hover:text-facet-highlight"
              >
                Decision record {s.adr}
              </a>
            </div>
          </div>
        </div>
        <div className="lg:col-span-7 2xl:col-span-8">
          <CodePanel code={s.artifact.code} lang={s.artifact.lang} title={s.artifact.title} />
        </div>
      </div>
    </div>
  );
}
