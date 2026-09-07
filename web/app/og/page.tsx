import type { Metadata } from "next";
import { caption } from "@/content/readouts";
import { site } from "@/content/site";

export const metadata: Metadata = {
  title: "kiln",
  robots: { index: false, follow: false },
};

// The social card, captured headless at 1200x630 into app/opengraph-image.png by
// `pnpm og`. The lattice is the same geometry and camera as the hero's fallback SVG.
export default function OgPage() {
  return (
    <div className="flex h-[630px] w-[1200px] items-center justify-between overflow-hidden bg-ink px-20">
      <div>
        <p className="text-3xl font-semibold tracking-tight text-fg">kiln</p>
        <h1 className="mt-6 max-w-[26rem] text-[2.6rem] font-light leading-[1.05] tracking-[-0.02em] text-fg">
          {site.headlineLines.map((line) => (
            <span key={line} className="block">
              {line}
            </span>
          ))}
        </h1>
        <p className="mt-6 max-w-[26rem] text-base text-fg-faint">{caption}</p>
      </div>
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src="/hero/lattice.svg" alt="" width={560} height={560} className="h-[560px] w-[560px]" />
    </div>
  );
}
