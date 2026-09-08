"use client";

import { useEffect, useRef, type ReactNode } from "react";
import TitleBlock from "./TitleBlock";

interface Props {
  n: number;
  title: string;
  drawnBy: string;
  lede?: ReactNode;
  children: ReactNode;
}

// A numbered drawing sheet: double border, body, title block. It draws itself in once,
// plotter-style, the first time it enters the viewport. The reveal attribute is set on the
// element directly, so without JavaScript the sheet is simply there.
export default function Sheet({ n, title, drawnBy, lede, children }: Props) {
  const ref = useRef<HTMLElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      el.dataset.reveal = "in";
      return;
    }
    el.dataset.reveal = "";
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting || e.boundingClientRect.bottom < 0)) {
          el.dataset.reveal = "in";
          io.disconnect();
        }
      },
      { rootMargin: "0px 0px -10% 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  return (
    <section ref={ref} id={`sheet-${n}`} className="sheet mx-auto my-4 md:my-[1.5vw]">
      <div className="sheet-inner">
        <div className="sheet-body">
          <div className="md:grid md:grid-cols-12 md:gap-8">
            <h2 className="sheet-title md:col-span-5">{title}</h2>
            {lede && <div className="sheet-lede mt-3 md:col-span-6 md:col-start-7 md:mt-1">{lede}</div>}
          </div>
          {children}
        </div>
        <TitleBlock n={n} title={title} drawnBy={drawnBy} />
      </div>
    </section>
  );
}
