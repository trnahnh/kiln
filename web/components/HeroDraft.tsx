import DraftScene from "./DraftScene";
import ReplayIntro from "./ReplayIntro";
import { site } from "@/content/site";
import { caption, source } from "@/content/readouts";

export const HERO_SLOT_ID = "hero-slot";

// Sheet 1. On desktop it fills the rest of the first viewport under the header; the
// drawing lands in the slot at hand-off and the headline is a note on the sheet.
export default function HeroDraft() {
  return (
    <section className="hero-copy relative z-[1] mx-auto flex w-full flex-col md:min-h-0 md:flex-1">
      <div className="sheet arrive mt-3 flex w-full flex-col md:mt-4 md:min-h-0 md:flex-1" style={{ "--arrive-delay": "0ms" } as React.CSSProperties}>
        <div className="sheet-inner flex flex-col md:min-h-0 md:flex-1">
          <div className="relative flex flex-col md:min-h-0 md:flex-1">
            <div className="relative h-[min(64svh,720px)] min-h-[360px] md:h-auto md:min-h-0 md:flex-1">
              {/* The rest-fit rect: on desktop the right part of the sheet, leaving the note its corner. */}
              <div id={HERO_SLOT_ID} className="hero-slot absolute inset-x-[3%] bottom-[4%] top-[3%] md:left-[32%] md:right-[2%] md:bottom-[5%] md:top-[2%]">
                {/* Shown when the scene cannot run; DraftScene hides it once the drawing is live. A plain
                    img: it is a committed SVG, and next/image would only add a loader round-trip. */}
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img src="/draft/drawing.svg" alt="" width={1600} height={1000} decoding="async" />
              </div>
            </div>
            <div className="hero-note relative mx-2 mb-2 md:absolute md:bottom-[5%] md:left-[2.5%] md:m-0 md:max-w-[30rem]">
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
