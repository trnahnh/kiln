"use client";

import FacetGlyph from "./FacetGlyph";
import type { NodeId } from "@/lib/hero/params";

export interface BandCell {
  id: NodeId;
  value: string;
  label: string;
}

function glow(node: number | null) {
  window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node } }));
}

// Six readouts under the hero, one per node. Hovering one brightens its node in the
// lattice while the hero is in view.
export default function ReadoutBand({ cells }: { cells: BandCell[] }) {
  return (
    <ul className="band">
      {cells.map((c, i) => (
        <li key={c.id} className="band-cell" onPointerEnter={() => glow(i)} onPointerLeave={() => glow(null)}>
          <span className="flex items-center gap-1.5 text-[12px] text-fg-faint">
            <FacetGlyph className="h-3.5 w-3.5 text-accent" />
            {c.id}
          </span>
          <span className="mt-2 block text-[1.375rem] font-semibold leading-none tracking-tight text-fg tabular-nums md:text-2xl">
            {c.value}
          </span>
          <span className="mt-1.5 block text-[13px] leading-snug text-fg-muted">{c.label}</span>
        </li>
      ))}
    </ul>
  );
}
