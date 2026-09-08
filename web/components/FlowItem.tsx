"use client";

import FacetGlyph from "./FacetGlyph";

interface Props {
  index: number;
  title: string;
  problem: string;
  value: string;
  detail: string;
}

function glow(node: number | null) {
  window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node } }));
}

export default function FlowItem({ index, title, problem, value, detail }: Props) {
  return (
    <li
      className="group relative grid grid-cols-1 gap-3 md:grid-cols-12 md:gap-8"
      onPointerEnter={() => glow(index)}
      onPointerLeave={() => glow(null)}
    >
      <span className="absolute -left-8 top-1 text-accent transition-colors group-hover:text-facet-highlight">
        <FacetGlyph />
      </span>
      <div className="md:col-span-5">
        <h3 className="text-lg font-semibold tracking-tight text-fg">{title}</h3>
        <p className="measure mt-2 text-[15px] leading-relaxed text-fg-muted">{problem}</p>
      </div>
      <div className="md:col-span-6 md:col-start-7">
        <p className="text-[1.75rem] font-semibold leading-none tracking-tight text-fg tabular-nums">{value}</p>
        <p className="measure mt-2 text-[15px] leading-relaxed text-fg-muted">{detail}</p>
      </div>
    </li>
  );
}
