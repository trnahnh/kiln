import DraftScene from "./DraftScene";
import ReplayIntro from "./ReplayIntro";
import { site } from "@/content/site";
import { caption, source } from "@/content/readouts";

export const HERO_SLOT_ID = "hero-slot";

// Sheet 1. The drawing lands in the slot at hand-off; the headline is a note on the sheet.
export default function HeroDraft() {
  return (
    <section className="hero-copy relative z-[1] mx-auto w-full max-w-[80rem]">
      <div className="sheet arrive mx-auto mt-4 md:mt-6" style={{ "--arrive-delay": "0ms" } as React.CSSProperties}>
        <div className="sheet-inner">
          <div className="relative">
            <div className="relative h-[min(64svh,720px)] min-h-[360px] md:h-[min(78svh,900px)] md:min-h-[520px]">
              {/* The rest-fit rect: on desktop the right part of the sheet, leaving the note its corner. */}
              <div id={HERO_SLOT_ID} className="hero-slot absolute inset-x-[3%] bottom-[4%] top-[3%] md:left-[32%] md:right-[2%] md:bottom-[5%] md:top-[2%]">
                {/* Shown when the scene cannot run; DraftScene hides it once the drawing is live. A plain
                    img: it is a committed SVG, and next/image would only add a loader round-trip. */}
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img src="/draft/drawing.svg" alt="" width={1600} height={1000} decoding="async" />
              </div>
            </div>
            <div className="hero-note relative mx-3 mb-3 md:absolute md:bottom-8 md:left-8 md:m-0 md:max-w-[30rem]">
              <h1 className="headline text-[2.6rem] md:text-[3.3rem]">
                {site.headlineLines.map((line) => (
                  <span key={line} className="block">
                    {line}
                  </span>
                ))}
              </h1>
              <p className="measure mt-4 text-[15px] leading-relaxed text-fg-muted md:text-base">{site.subline}</p>
              <p className="mono mt-3 text-[12px] text-fg-faint">{caption}</p>
              <div className="mt-5 flex flex-wrap items-center gap-x-5 gap-y-3">
                <a href={site.actions.primary.href} className="btn">
                  {site.actions.primary.label}
                </a>
                <a href={source.runUrl} className="link text-sm">
                  {site.actions.secondary.label}
                </a>
                <ReplayIntro variant="inline" />
              </div>
            </div>
          </div>
        </div>
      </div>
      <DraftScene slotId={HERO_SLOT_ID} />
    </section>
  );
}
