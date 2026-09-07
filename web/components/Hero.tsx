import type { ReactNode } from "react";
import HeroScene from "./HeroScene";
import ReplayIntro from "./ReplayIntro";
import { site } from "@/content/site";
import { caption, source } from "@/content/readouts";

export const HERO_SLOT_ID = "hero-slot";

export default function Hero({ band }: { band?: ReactNode }) {
  return (
    <section className="hero-copy relative z-[1] mx-auto flex w-full max-w-6xl flex-col px-6 md:min-h-[calc(100svh-4.5rem)] md:px-8">
      <div className="grid flex-1 grid-cols-1 items-center gap-8 pb-10 pt-6 md:grid-cols-12 md:gap-8 md:pb-12">
        <div className="md:col-span-6">
          <h1 className="arrive measure text-[2.5rem] font-light leading-[1.05] tracking-[-0.02em] text-fg md:text-[3.25rem] xl:text-[3.75rem]">
            {site.headlineLines.map((line) => (
              <span key={line} className="block">
                {line}
              </span>
            ))}
          </h1>
          <p className="arrive measure mt-6 max-w-[38rem] text-base leading-relaxed text-fg-muted md:text-lg" style={{ "--arrive-delay": "80ms" } as React.CSSProperties}>
            {site.subline}
          </p>
          <div className="arrive mt-9 flex flex-wrap items-center gap-x-6 gap-y-3 text-sm" style={{ "--arrive-delay": "160ms" } as React.CSSProperties}>
            <a href={site.actions.primary.href} className="rounded-sm bg-fg px-4 py-2.5 font-semibold text-ink transition-colors hover:bg-facet-highlight">
              {site.actions.primary.label}
            </a>
            <a href={source.runUrl} className="px-1 py-2.5 text-fg-muted underline decoration-hairline-strong underline-offset-4 transition-colors hover:text-fg">
              {site.actions.secondary.label}
            </a>
          </div>
        </div>
        <div className="order-first md:order-none md:col-span-6 md:col-start-7">
          <div className="arrive" style={{ "--arrive-delay": "240ms" } as React.CSSProperties}>
            <div id={HERO_SLOT_ID} className="hero-slot mx-auto max-h-[46svh] md:max-h-[62svh]">
              {/* Shown when the scene cannot run; HeroScene hides it once WebGL is drawing. A plain
                  img: it is a committed SVG, and next/image would only add a loader round-trip. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src="/hero/lattice.svg" alt="" width={1000} height={1000} decoding="async" />
            </div>
            <div className="mt-6 text-center md:mt-8">
              <p className="text-sm text-fg-faint">{caption}</p>
              <ReplayIntro variant="inline" />
            </div>
          </div>
        </div>
      </div>
      {band && (
        <div className="arrive pb-8 md:pb-10" style={{ "--arrive-delay": "320ms" } as React.CSSProperties}>
          {band}
        </div>
      )}
      <HeroScene slotId={HERO_SLOT_ID} />
    </section>
  );
}
