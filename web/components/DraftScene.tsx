"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import type { DraftModel, Vec2 } from "@/lib/draft/model";
import { pointAlong } from "@/lib/draft/model";
import { layoutDrawing, type Drawing, type Prim } from "@/lib/draft/layout";
import { ISO, solveFit, toScreen, type Fit } from "@/lib/draft/project";
import { ORBIT, landscapeBeats, linear, smoothstep, warpedTime, type Beats } from "@/lib/draft/timeline";
import {
  hasSeenThisSession,
  isHardReload,
  logSkip,
  markSeen,
  prefersReducedMotion,
  releaseArrivals,
} from "@/lib/hero/gate";

type Phase = "loading" | "intro" | "rest" | "static";
type LayoutName = "landscape" | "portrait";

const ASSET = "/draft/model.json";
const ASSET_TIMEOUT_MS = 2500;
const PHONE = "(max-width: 639px)";
const SHEET_INSET = 22;
const PHONE_INSET = 10;
const TEXT_REF_SCALE = 72;
const TEXT_PX = { label: 20, stack: 11, dimvalue: 24, dimlabel: 11 } as const;
const TITLE_H = 84;
const TITLE_W = 620;
const TRACK_ZOOM = 1.5;
const TRACK_LEAD = 0.06;
const TRACK_EYE = 0.5;

interface Props {
  slotId: string;
}

function subscribeNothing() {
  return () => {};
}

function pathOf(points: Vec2[]): string {
  let d = "";
  for (let i = 0; i < points.length; i++) d += (i === 0 ? "M" : "L") + points[i][0].toFixed(1) + " " + points[i][1].toFixed(1);
  return d;
}

function mixFit(a: Fit, b: Fit, t: number): Fit {
  return {
    scale: a.scale + (b.scale - a.scale) * t,
    offsetX: a.offsetX + (b.offsetX - a.offsetX) * t,
    offsetY: a.offsetY + (b.offsetY - a.offsetY) * t,
  };
}

export default function DraftScene({ slotId }: Props) {
  const mounted = useSyncExternalStore(subscribeNothing, () => true, () => false);
  const [phase, setPhase] = useState<Phase>("loading");
  const [model, setModel] = useState<DraftModel | null>(null);
  const [layoutName, setLayoutName] = useState<LayoutName>("landscape");
  const stageRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const hintRef = useRef<HTMLDivElement>(null);
  const phaseRef = useRef<Phase>("loading");
  const modelRef = useRef<DraftModel | null>(null);

  useEffect(() => {
    if (!mounted) return;
    const stage = stageRef.current;
    const backdrop = backdropRef.current;
    if (!stage || !backdrop) return;

    let disposed = false;
    let raf = 0;
    let inView = true;
    let viewport = { w: 1, h: 1 };
    let inset = SHEET_INSET;
    let introFit: Fit | null = null;
    let trackFit: Fit | null = null;
    let restFit: Fit | null = null;
    let base: Drawing | null = null;
    let beats: Beats = landscapeBeats();
    let start = 0;
    let pausedAt: number | null = null;
    let skipAt: number | null = null;
    let tAtSkip = 0;
    let t = 0;
    let released = false;
    let scrollLocked = false;
    const orbit = { x: 0, y: 0, tx: 0, ty: 0 };
    let glowBlock = -1;

    const isPhone = () => window.matchMedia(PHONE).matches;
    const finePointer = () => window.matchMedia("(pointer: fine)").matches;
    const currentLayout = (): LayoutName => (isPhone() || window.innerWidth / window.innerHeight < 0.8 ? "portrait" : "landscape");

    const setPhaseBoth = (p: Phase) => {
      phaseRef.current = p;
      setPhase(p);
    };
    const lockScroll = (on: boolean) => {
      if (on === scrollLocked) return;
      scrollLocked = on;
      document.documentElement.style.overflow = on ? "hidden" : "";
    };
    const bail = (reason: string) => {
      logSkip(reason);
      releaseArrivals();
      lockScroll(false);
      setPhaseBoth("static");
    };

    const slotRect = () => {
      const el = document.getElementById(slotId);
      if (!el) return { x: viewport.w * 0.1, y: viewport.h * 0.1, w: viewport.w * 0.8, h: viewport.h * 0.7 };
      const r = el.getBoundingClientRect();
      return { x: r.left, y: r.top + window.scrollY, w: r.width, h: r.height };
    };

    const layout = () => {
      const m = modelRef.current;
      if (!m) return;
      const w = stage.clientWidth || window.innerWidth;
      const h = stage.clientHeight || window.innerHeight;
      viewport = { w, h };
      const name = currentLayout();
      inset = name === "portrait" ? PHONE_INSET : SHEET_INSET;
      setLayoutName(name);
      base = layoutDrawing(m, name, ISO);
      beats = base.beats;
      const titleRoom = name === "portrait" ? TITLE_H * 2 + 16 : TITLE_H;
      introFit = solveFit(base.bounds, {
        x: inset + 24,
        y: inset + 24,
        w: w - inset * 2 - 48,
        h: h - inset * 2 - titleRoom - 48,
      }, 0.84);
      // The tracking shot fits the drawing to the sheet's width, then zooms in; the camera's
      // vertical position is chosen per frame from where the request is.
      const byWidth = solveFit(base.bounds, { x: inset + 12, y: 0, w: w - inset * 2 - 24, h: 1e9 }, 0.9);
      trackFit = { scale: byWidth.scale * TRACK_ZOOM, offsetX: w / 2 - ((base.bounds.minX + base.bounds.maxX) / 2) * byWidth.scale * TRACK_ZOOM, offsetY: 0 };
      restFit = solveFit(base.bounds, slotRect(), 0.96);
      schedule();
    };

    const currentTime = (now: number) => (skipAt !== null ? warpedTime(tAtSkip, (now - skipAt) / 1000, beats.done) : (now - start) / 1000);

    const cameraFit = (time: number, drawing: Drawing): Fit => {
      if (!introFit || !restFit || !trackFit) return { scale: 1, offsetX: 0, offsetY: 0 };
      const handoff = smoothstep(beats.handoffStart, beats.handoffEnd, time);
      if (!beats.tracking) return mixFit(introFit, restFit, handoff);
      const f = Math.min(1, linear(beats.travelStart, beats.travelEnd, time) + TRACK_LEAD);
      const focus = pointAlong(drawing.travel, f);
      const tracking: Fit = { ...trackFit, offsetY: viewport.h * TRACK_EYE - focus[1] * trackFit.scale };
      const pull = smoothstep(beats.pullbackStart, beats.pullbackEnd, time);
      return mixFit(tracking, restFit, pull);
    };

    const renderAt = (time: number) => {
      const m = modelRef.current;
      const svg = svgRef.current;
      if (!m || !svg || !introFit || !restFit || !base) return;
      const name = currentLayout();
      const cam = { yaw: ISO.yaw + orbit.x * ORBIT.yaw, pitch: ISO.pitch + orbit.y * ORBIT.pitch };
      const drawing = orbit.x === 0 && orbit.y === 0 ? base : layoutDrawing(m, name, cam);
      const h = smoothstep(beats.handoffStart, beats.handoffEnd, time);
      const fit = cameraFit(time, drawing);
      const S = (p: Vec2) => toScreen(p, fit);
      // Text does not scale with the projection, so it is sized from the fit instead, and the
      // small annotations drop out when the drawing is too small to carry them.
      const k = Math.max(0.5, Math.min(1.1, fit.scale / TEXT_REF_SCALE));

      for (const p of drawing.prims) {
        const el = svg.querySelector<SVGElement>(`[data-id="${p.id}"]`);
        if (!el) continue;
        const progress = smoothstep(p.t0, p.t1, time);
        if (p.points) {
          el.setAttribute("d", pathOf(p.points.map(S)));
          if (p.kind === "hidden" || p.kind === "grid") {
            el.style.opacity = (progress * (p.kind === "grid" ? 1 : 0.6)).toFixed(3);
          } else {
            el.style.strokeDashoffset = (1 - progress).toFixed(4);
          }
        } else if (p.at) {
          const [x, y] = S(p.at);
          const dy = p.kind === "label" ? -6 : p.kind === "stack" ? 4 : p.kind === "dimvalue" ? -14 : -2;
          el.setAttribute("x", x.toFixed(1));
          el.setAttribute("y", (y + dy * k).toFixed(1));
          el.style.fontSize = (TEXT_PX[p.kind as keyof typeof TEXT_PX] * k).toFixed(1) + "px";
          const tooSmall = k < 0.6 && (p.kind === "dimlabel" || p.kind === "stack");
          el.style.opacity = tooSmall ? "0" : progress.toFixed(3);
        }
      }

      m.blocks.forEach((_, i) => {
        const hit = svg.querySelector<SVGRectElement>(`[data-hit="${i}"]`);
        const group = svg.querySelector<SVGGElement>(`[data-block="${i}"]`);
        if (!hit || !group) return;
        const pts = drawing.prims.filter((p) => p.block === i && p.points).flatMap((p) => p.points!.map(S));
        const xs = pts.map((p) => p[0]);
        const ys = pts.map((p) => p[1]);
        hit.setAttribute("x", Math.min(...xs).toFixed(1));
        hit.setAttribute("y", Math.min(...ys).toFixed(1));
        hit.setAttribute("width", (Math.max(...xs) - Math.min(...xs)).toFixed(1));
        hit.setAttribute("height", (Math.max(...ys) - Math.min(...ys)).toFixed(1));
        group.dataset.glow = glowBlock === i ? "1" : "0";
      });

      const f = linear(beats.travelStart, beats.travelEnd, time);
      const dot = svg.querySelector<SVGCircleElement>("[data-travel]");
      const trail = svg.querySelector<SVGPathElement>("[data-trail]");
      if (dot && trail) {
        const pos = S(pointAlong(drawing.travel, f));
        dot.setAttribute("cx", pos[0].toFixed(1));
        dot.setAttribute("cy", pos[1].toFixed(1));
        dot.setAttribute("r", (5 * Math.max(1, k)).toFixed(1));
        dot.style.opacity = time >= beats.travelStart ? "1" : "0";
        const steps = 12;
        const trailPts: Vec2[] = [];
        for (let s = 0; s <= steps; s++) trailPts.push(S(pointAlong(drawing.travel, Math.max(0, f - 0.1) + (0.1 * s) / steps)));
        trail.setAttribute("d", pathOf(trailPts));
        trail.style.opacity = f > 0 && f < 1 ? "0.8" : "0";
      }

      const clip = svg.querySelector<SVGRectElement>("[data-clip]");
      if (clip) {
        const intro = phaseRef.current === "intro" && h < 1;
        clip.setAttribute("x", String(intro ? inset : -1e5));
        clip.setAttribute("y", String(intro ? inset : -1e5));
        clip.setAttribute("width", String(intro ? viewport.w - inset * 2 : 2e5));
        clip.setAttribute("height", String(intro ? viewport.h - inset * 2 : 2e5));
      }

      const chrome = svg.querySelector<SVGGElement>("[data-chrome]");
      if (chrome) {
        chrome.style.opacity = (1 - h).toFixed(3);
        const border = chrome.querySelector<SVGRectElement>("[data-border]");
        if (border) {
          border.setAttribute("x", String(inset));
          border.setAttribute("y", String(inset));
          border.setAttribute("width", String(viewport.w - inset * 2));
          border.setAttribute("height", String(viewport.h - inset * 2));
          border.style.strokeDashoffset = (1 - smoothstep(beats.borderStart, beats.borderEnd, time)).toFixed(4);
        }
        const tb = chrome.querySelector<SVGGElement>("[data-titleblock]");
        if (tb) {
          const tw = Math.min(TITLE_W, viewport.w - inset * 2);
          tb.setAttribute("transform", `translate(${viewport.w - inset - tw} ${viewport.h - inset - TITLE_H})`);
          tb.querySelector<SVGRectElement>("[data-tbrect]")?.setAttribute("width", String(tw));
          tb.querySelector<SVGLineElement>("[data-tbmid]")?.setAttribute("x2", String(tw));
          tb.style.opacity = smoothstep(beats.borderStart + 0.2, beats.borderEnd + 0.3, time).toFixed(3);
          const stamp = chrome.querySelector<SVGGElement>("[data-stamp]");
          if (stamp) {
            const s = smoothstep(beats.stampStart, beats.stampEnd, time);
            const scale = 1.5 - 0.5 * s;
            const stampX = beats.tracking ? viewport.w / 2 : viewport.w - inset - tw - 110;
            const stampY = beats.tracking ? viewport.h - inset - TITLE_H - 40 : viewport.h - inset - TITLE_H / 2;
            stamp.setAttribute("transform", `translate(${stampX} ${stampY}) rotate(-8) scale(${scale.toFixed(3)})`);
            stamp.style.opacity = s.toFixed(3);
          }
        }
      }
    };

    const settle = () => {
      let moving = false;
      const ease = (cur: number, target: number) => {
        const next = cur + (target - cur) * 0.12;
        if (Math.abs(next - target) > 0.0005) moving = true;
        return Math.abs(next - target) <= 0.0005 ? target : next;
      };
      orbit.x = ease(orbit.x, orbit.tx);
      orbit.y = ease(orbit.y, orbit.ty);
      return moving;
    };

    const finishIntro = () => {
      setPhaseBoth("rest");
      lockScroll(false);
      backdrop.style.opacity = "0";
      if (hintRef.current) hintRef.current.style.opacity = "0";
      window.dispatchEvent(new Event("intro-done"));
    };

    const tick = (now: number) => {
      raf = 0;
      if (disposed) return;
      let again = false;
      if (phaseRef.current === "intro") {
        if (pausedAt === null) {
          t = currentTime(now);
          if (t >= beats.handoffStart && !released) {
            released = true;
            releaseArrivals();
            // The page has laid out by now; measure the slot again so the drawing lands exactly.
            restFit = base ? solveFit(base.bounds, slotRect(), 0.96) : restFit;
          }
          backdrop.style.opacity = (1 - smoothstep(beats.handoffStart, beats.handoffEnd, t)).toFixed(3);
          if (hintRef.current) {
            const show = skipAt === null && t >= 1.0 && t < beats.handoffStart ? smoothstep(1.0, 1.6, t) : 0;
            hintRef.current.style.opacity = show.toFixed(3);
          }
          if (t >= beats.done) {
            t = beats.done;
            finishIntro();
          } else {
            again = true;
          }
        }
      } else if (phaseRef.current === "rest") {
        t = beats.done;
      }
      const moving = settle();
      if (inView || phaseRef.current === "intro") renderAt(t);
      if (again || moving) schedule();
    };
    const schedule = () => {
      if (raf === 0 && !disposed) raf = requestAnimationFrame(tick);
    };

    const skip = () => {
      if (phaseRef.current !== "intro" || skipAt !== null || pausedAt !== null) return;
      tAtSkip = t;
      skipAt = performance.now();
      if (hintRef.current) hintRef.current.style.opacity = "0";
      schedule();
    };
    const beginIntro = () => {
      window.scrollTo(0, 0);
      lockScroll(true);
      skipAt = null;
      pausedAt = null;
      released = false;
      backdrop.style.opacity = "1";
      start = performance.now();
      t = 0;
      setPhaseBoth("intro");
      window.dispatchEvent(new Event("intro-shown"));
      stage.focus({ preventScroll: true });
      schedule();
    };

    const onPointerDown = () => skip();
    const onKeyDown = (e: KeyboardEvent) => {
      if (phaseRef.current !== "intro" || e.key === "Tab") return;
      e.preventDefault();
      skip();
    };
    const onWheel = (e: WheelEvent) => {
      if (phaseRef.current !== "intro") return;
      e.preventDefault();
      skip();
    };
    const onTouchMove = (e: TouchEvent) => {
      if (phaseRef.current !== "intro") return;
      e.preventDefault();
      skip();
    };
    const onPointerMove = (e: PointerEvent) => {
      if (phaseRef.current !== "rest" || !finePointer() || !inView) return;
      orbit.tx = (e.clientX / window.innerWidth) * 2 - 1;
      orbit.ty = (e.clientY / window.innerHeight) * 2 - 1;
      schedule();
    };
    const onPointerLeave = () => {
      orbit.tx = 0;
      orbit.ty = 0;
      schedule();
    };
    const onGlow = (e: Event) => {
      const node = (e as CustomEvent<{ node: number | null }>).detail?.node;
      glowBlock = typeof node === "number" ? node : -1;
      schedule();
    };
    const onVisibility = () => {
      if (phaseRef.current !== "intro") return;
      if (document.hidden) {
        pausedAt = performance.now();
      } else if (pausedAt !== null) {
        const delta = performance.now() - pausedAt;
        start += delta;
        if (skipAt !== null) skipAt += delta;
        pausedAt = null;
        schedule();
      }
    };
    const onReplay = () => {
      if (modelRef.current) beginIntro();
    };

    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("wheel", onWheel, { passive: false });
    window.addEventListener("touchmove", onTouchMove, { passive: false });
    window.addEventListener("pointermove", onPointerMove);
    document.documentElement.addEventListener("pointerleave", onPointerLeave);
    window.addEventListener("hero-glow", onGlow);
    window.addEventListener("replay-intro", onReplay);
    document.addEventListener("visibilitychange", onVisibility);
    stage.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("resize", layout);
    const io = new IntersectionObserver((entries) => {
      inView = entries.some((e) => e.isIntersecting);
      if (inView) schedule();
    });
    io.observe(stage);

    const run = async () => {
      if (prefersReducedMotion()) {
        bail("prefers-reduced-motion is set");
        return;
      }
      // Same decision as the inline gate script in app/layout.tsx (GATE_SCRIPT).
      const playIntro = isHardReload() || !hasSeenThisSession();
      if (!playIntro) logSkip("already seen this session");

      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), ASSET_TIMEOUT_MS);
      let m: DraftModel;
      try {
        const res = await fetch(ASSET, { signal: controller.signal });
        if (!res.ok) throw new Error(`asset returned ${res.status}`);
        m = (await res.json()) as DraftModel;
      } catch (err) {
        clearTimeout(timer);
        const aborted = err instanceof DOMException && err.name === "AbortError";
        bail(aborted ? `asset not loaded within ${ASSET_TIMEOUT_MS / 1000} s` : `asset failed: ${String(err)}`);
        return;
      }
      clearTimeout(timer);
      if (disposed) return;
      modelRef.current = m;
      setModel(m);
      const slot = document.getElementById(slotId);
      if (slot) slot.dataset.live = "1";
      requestAnimationFrame(() => {
        if (disposed) return;
        layout();
        if (playIntro) {
          markSeen();
          beginIntro();
        } else {
          releaseArrivals();
          t = beats.done;
          backdrop.style.opacity = "0";
          setPhaseBoth("rest");
          schedule();
        }
      });
    };
    void run();

    return () => {
      disposed = true;
      if (raf) cancelAnimationFrame(raf);
      lockScroll(false);
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("wheel", onWheel);
      window.removeEventListener("touchmove", onTouchMove);
      window.removeEventListener("pointermove", onPointerMove);
      document.documentElement.removeEventListener("pointerleave", onPointerLeave);
      window.removeEventListener("hero-glow", onGlow);
      window.removeEventListener("replay-intro", onReplay);
      document.removeEventListener("visibilitychange", onVisibility);
      stage.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("resize", layout);
      io.disconnect();
    };
  }, [mounted, slotId]);

  if (!mounted) return null;

  const intro = phase === "intro";
  const prims: Prim[] = model ? layoutDrawing(model, layoutName, ISO).prims : [];
  const selectBlock = (i: number) => {
    window.dispatchEvent(new CustomEvent("select-specimen", { detail: { node: i } }));
    document.getElementById("sheet-2")?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  return createPortal(
    <div
      ref={stageRef}
      className="hero-stage"
      data-phase={phase}
      role={intro ? "button" : undefined}
      tabIndex={intro ? 0 : -1}
      aria-label={intro ? "Skip the introduction" : undefined}
      aria-hidden={intro ? undefined : true}
      hidden={phase === "static"}
    >
      <div ref={backdropRef} className="hero-backdrop" />
      <svg ref={svgRef} className="draft-svg" aria-hidden="true">
        <defs>
          <clipPath id="draft-clip">
            <rect data-clip x="-100000" y="-100000" width="200000" height="200000" />
          </clipPath>
        </defs>
        <g clipPath="url(#draft-clip)">
          {prims.filter((p) => p.kind === "grid").map((p) => (
            <path key={p.id} data-id={p.id} fill="none" stroke="var(--line-hair)" strokeWidth="1" style={{ opacity: 0 }} />
          ))}
          {prims.filter((p) => p.kind === "pipe").map((p) => (
            <path key={p.id} data-id={p.id} fill="none" stroke="var(--line)" strokeWidth="2.4" pathLength={1} strokeDasharray="1" style={{ strokeDashoffset: 1 }} />
          ))}
          {model?.blocks.map((b, i) => (
            <g key={b.id} data-block={i} data-glow="0">
              {prims.filter((p) => p.block === i && p.kind === "hidden").map((p) => (
                <path key={p.id} data-id={p.id} fill="none" stroke="var(--line)" strokeWidth="1" strokeDasharray="6 5" style={{ opacity: 0 }} />
              ))}
              {prims.filter((p) => p.block === i && p.kind === "edge").map((p) => (
                <path key={p.id} data-id={p.id} fill="none" stroke="var(--line)" strokeWidth="1.6" pathLength={1} strokeDasharray="1" style={{ strokeDashoffset: 1 }} />
              ))}
              {prims.filter((p) => p.block === i && p.kind === "dim").map((p) => (
                <path key={p.id} data-id={p.id} fill="none" stroke="var(--accent)" strokeWidth="1.2" pathLength={1} strokeDasharray="1" style={{ strokeDashoffset: 1 }} />
              ))}
              {prims.filter((p) => p.block === i && p.at).map((p) => (
                <text
                  key={p.id}
                  data-id={p.id}
                  className={p.kind === "label" ? "dr-label" : p.kind === "stack" ? "dr-stack" : p.kind === "dimvalue" ? "dr-value" : "dr-measure"}
                  style={{ opacity: 0 }}
                >
                  {p.text}
                </text>
              ))}
              <rect
                data-hit={i}
                className="dr-hit"
                onPointerEnter={() => window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node: i } }))}
                onPointerLeave={() => window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node: null } }))}
                onClick={() => selectBlock(i)}
              />
            </g>
          ))}
          <path data-trail fill="none" stroke="var(--accent)" strokeWidth="3" strokeLinecap="round" style={{ opacity: 0 }} />
          <circle data-travel r="5" fill="var(--accent)" style={{ opacity: 0 }} />
        </g>
        <g data-chrome>
          <rect data-border fill="none" stroke="var(--line)" strokeWidth="1.5" pathLength={1} strokeDasharray="1" style={{ strokeDashoffset: 1 }} />
          <g data-titleblock style={{ opacity: 0 }}>
            <rect data-tbrect x="0" y="0" height={TITLE_H} fill="var(--ground)" stroke="var(--line)" strokeWidth="1.5" />
            <line data-tbmid x1="0" y1={TITLE_H / 2} x2="0" y2={TITLE_H / 2} stroke="var(--line)" />
            <line x1="150" y1="0" x2="150" y2={TITLE_H} stroke="var(--line)" />
            <text x="12" y="17" className="tb-svg-k">PROJECT</text>
            <text x="12" y="36" className="tb-svg-v tb-svg-big">KILN</text>
            <text x="162" y="17" className="tb-svg-k">TITLE</text>
            <text x="162" y="36" className="tb-svg-v">{model ? `${model.title}, ${model.subtitle}` : ""}</text>
            <text x="12" y={TITLE_H / 2 + 17} className="tb-svg-k">SHEET</text>
            <text x="12" y={TITLE_H / 2 + 36} className="tb-svg-v">1 OF 6</text>
            <text x="162" y={TITLE_H / 2 + 17} className="tb-svg-k">REV</text>
            <text x="162" y={TITLE_H / 2 + 36} className="tb-svg-v">{model?.rev ?? ""}</text>
            <text x="300" y={TITLE_H / 2 + 17} className="tb-svg-k">CHECKED</text>
            <text x="300" y={TITLE_H / 2 + 36} className="tb-svg-v tb-svg-accent">{model?.checked ?? ""}</text>
          </g>
          <g data-stamp style={{ opacity: 0 }}>
            <rect x="-62" y="-20" width="124" height="40" fill="none" stroke="var(--accent)" strokeWidth="2.5" />
            <text x="0" y="8" className="dr-stamp">CHECKED</text>
          </g>
        </g>
      </svg>
      {intro && (
        <div ref={hintRef} className="hero-hint" style={{ opacity: 0 }} aria-hidden="true">
          Press any key to skip
        </div>
      )}
    </div>,
    document.body,
  );
}
