"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { NODE_IDS, type LatticeParams } from "@/lib/hero/params";
import { buildMesh } from "@/lib/hero/geometry";
import { HeroRenderer, cssColor, type SceneColors } from "@/lib/hero/renderer";
import { solveFit, cameraMatrices, projectToNdc, type Fit, type Camera } from "@/lib/hero/camera";
import { TL, cameraPoseAt, handoffAt, lightDirAt, warpedTime, smoothstep } from "@/lib/hero/timeline";
import { scale, add, anyPerpendicular } from "@/lib/hero/math";
import {
  hasSeenThisSession,
  isHardReload,
  logSkip,
  markSeen,
  prefersReducedMotion,
  releaseArrivals,
} from "@/lib/hero/gate";

type Phase = "loading" | "intro" | "rest" | "static";

const ASSET = "/hero/lattice.json";
const ASSET_TIMEOUT_MS = 2500;
const PARALLAX = (4 * Math.PI) / 180;
const LABEL_REACH = 1.55;
const LABEL_GAP_PX = 14;
const CLUSTER_REACH = 0.62;
const PHONE = "(max-width: 639px)";

interface Props {
  slotId: string;
}

function subscribeNothing() {
  return () => {};
}

export default function HeroScene({ slotId }: Props) {
  const mounted = useSyncExternalStore(subscribeNothing, () => true, () => false);
  const [phase, setPhase] = useState<Phase>("loading");
  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const hintRef = useRef<HTMLDivElement>(null);
  const labelRefs = useRef<(HTMLSpanElement | null)[]>([]);
  const phaseRef = useRef<Phase>("loading");

  useEffect(() => {
    if (!mounted) return;
    const stage = stageRef.current;
    const canvas = canvasRef.current;
    const backdrop = backdropRef.current;
    if (!stage || !canvas || !backdrop) return;

    let renderer: HeroRenderer | null = null;
    let params: LatticeParams | null = null;
    let introFit: Fit | null = null;
    let restFit: Fit | null = null;
    let viewport = { w: 1, h: 1 };
    let disposed = false;
    let raf = 0;
    let inView = true;

    let start = 0;
    let pausedAt: number | null = null;
    let skipAt: number | null = null;
    let tAtSkip = 0;
    let t = 0;
    let released = false;
    let scrollLocked = false;

    const parallax = { x: 0, y: 0, tx: 0, ty: 0 };
    const glow = new Float32Array(6);
    const glowTarget = new Float32Array(6);
    const isPhone = () => window.matchMedia(PHONE).matches;
    const finePointer = () => window.matchMedia("(pointer: fine)").matches;

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

    const readColors = (): SceneColors => {
      const cs = getComputedStyle(document.documentElement);
      const read = (name: string) => cssColor(cs.getPropertyValue(name));
      return {
        ink: read("--scene-ink"),
        shadow: read("--facet-shadow"),
        deep: read("--facet-deep"),
        accent: read("--facet-accent"),
        highlight: read("--facet-highlight"),
      };
    };

    const slotRect = () => {
      const el = document.getElementById(slotId);
      if (!el) return { x: viewport.w * 0.55, y: viewport.h * 0.15, w: viewport.w * 0.4, h: viewport.h * 0.7 };
      const r = el.getBoundingClientRect();
      return { x: r.left, y: r.top + window.scrollY, w: r.width, h: r.height };
    };

    const layout = () => {
      if (!renderer || !params) return;
      const w = stage.clientWidth || window.innerWidth;
      const h = stage.clientHeight || window.innerHeight;
      viewport = { w, h };
      const dpr = Math.min(window.devicePixelRatio || 1, isPhone() ? 1 : 1.5);
      renderer.resize(Math.round(w * dpr), Math.round(h * dpr));
      introFit = solveFit(params.radius, params.camera.fov, viewport, { x: 0, y: 0, w, h }, 0.78);
      restFit = solveFit(params.radius, params.camera.fov, viewport, slotRect(), 0.84);
      requestFrame();
    };

    const currentTime = (now: number) => {
      if (skipAt !== null) return warpedTime(tAtSkip, (now - skipAt) / 1000);
      return (now - start) / 1000;
    };

    const placeLabels = (cam: Camera, time: number) => {
      if (!params) return;
      const phone = isPhone();
      params.nodes.forEach((node, i) => {
        const el = labelRefs.current[i];
        if (!el) return;
        // Push the label out along the node's screen-space direction from the lattice centre,
        // never closer than the cluster's projected radius, so a node facing the camera still
        // gets its label beside its crystals rather than on them.
        const toPx = (p: { x: number; y: number }) => [((p.x + 1) / 2) * viewport.w, ((1 - p.y) / 2) * viewport.h];
        const [nx, ny] = toPx(projectToNdc(cam, node.position));
        const [tx, ty] = toPx(projectToNdc(cam, scale(node.position, LABEL_REACH)));
        const [cx, cy] = toPx(projectToNdc(cam, [0, 0, 0]));
        const side = anyPerpendicular(node.position);
        const [sx, sy] = toPx(projectToNdc(cam, add(node.position, scale(side, CLUSTER_REACH))));
        const clusterPx = Math.hypot(sx - nx, sy - ny);
        let dx = nx - cx;
        let dy = ny - cy;
        const len = Math.hypot(dx, dy);
        if (len < 1) {
          dx = 0;
          dy = 1;
        } else {
          dx /= len;
          dy /= len;
        }
        const along = Math.max(Math.hypot(tx - nx, ty - ny), clusterPx) + LABEL_GAP_PX;
        const x = nx + dx * along;
        const y = ny + dy * along;
        const shown = phone
          ? smoothstep(TL.handoffStart, TL.handoffEnd, time)
          : smoothstep(node.lockAt, node.lockAt + 0.3, time);
        el.style.transform = `translate(${x.toFixed(1)}px, ${y.toFixed(1)}px) translate(-50%, -50%)`;
        el.style.opacity = shown.toFixed(3);
        el.dataset.glow = glow[i] > 0.5 ? "1" : "0";
      });
    };

    const renderAt = (time: number) => {
      if (!renderer || !params || !introFit || !restFit) return;
      const pose = cameraPoseAt(time);
      const h = handoffAt(time);
      const introDistance = introFit.distance * pose.distanceScale;
      const cam = cameraMatrices({
        fovY: params.camera.fov,
        aspect: viewport.w / viewport.h,
        distance: introDistance + (restFit.distance - introDistance) * h,
        yaw: params.camera.restYaw + pose.yawOffset + parallax.x * PARALLAX,
        pitch: params.camera.restPitch + parallax.y * PARALLAX,
        shiftX: introFit.shiftX + (restFit.shiftX - introFit.shiftX) * h,
        shiftY: introFit.shiftY + (restFit.shiftY - introFit.shiftY) * h,
        radius: params.radius,
      });
      renderer.render({ t: time, view: cam.view, proj: cam.proj, eye: cam.eye, light: lightDirAt(time), glow });
      placeLabels(cam, time);
    };

    const settle = () => {
      let moving = false;
      const ease = (cur: number, target: number) => {
        const next = cur + (target - cur) * 0.12;
        if (Math.abs(next - target) > 0.0005) moving = true;
        return Math.abs(next - target) <= 0.0005 ? target : next;
      };
      parallax.x = ease(parallax.x, parallax.tx);
      parallax.y = ease(parallax.y, parallax.ty);
      for (let i = 0; i < 6; i++) glow[i] = ease(glow[i], glowTarget[i]);
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
          if (t >= TL.handoffStart && !released) {
            released = true;
            releaseArrivals();
          }
          backdrop.style.opacity = (1 - handoffAt(t)).toFixed(3);
          if (hintRef.current) {
            const show = skipAt === null && t >= 1.0 && t < TL.handoffStart ? smoothstep(1.0, 1.6, t) : 0;
            hintRef.current.style.opacity = show.toFixed(3);
          }
          if (t >= TL.done) {
            t = TL.done;
            finishIntro();
          } else {
            again = true;
          }
        }
      } else if (phaseRef.current === "rest") {
        t = TL.done;
      }
      const moving = settle();
      if (inView || phaseRef.current === "intro") renderAt(t);
      if (again || moving) schedule();
    };

    const schedule = () => {
      if (raf === 0 && !disposed) raf = requestAnimationFrame(tick);
    };
    const requestFrame = () => schedule();

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
      if (phaseRef.current !== "intro") return;
      if (e.key === "Tab") return;
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
      parallax.tx = (e.clientX / window.innerWidth) * 2 - 1;
      parallax.ty = (e.clientY / window.innerHeight) * 2 - 1;
      schedule();
    };
    const onPointerLeave = () => {
      parallax.tx = 0;
      parallax.ty = 0;
      schedule();
    };
    const onGlow = (e: Event) => {
      const node = (e as CustomEvent<{ node: number | null }>).detail?.node;
      glowTarget.fill(0);
      if (typeof node === "number" && node >= 0 && node < 6) glowTarget[node] = 1;
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
      if (!renderer) return;
      beginIntro();
    };
    const onThemeChange = () => {
      if (renderer) {
        renderer.setColors(readColors());
        schedule();
      }
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
    const themeMedia = window.matchMedia("(prefers-color-scheme: dark)");
    themeMedia.addEventListener("change", onThemeChange);
    const themeObserver = new MutationObserver(onThemeChange);
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "data-theme"] });
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
      try {
        const res = await fetch(ASSET, { signal: controller.signal });
        if (!res.ok) throw new Error(`asset returned ${res.status}`);
        params = (await res.json()) as LatticeParams;
      } catch (err) {
        clearTimeout(timer);
        const aborted = err instanceof DOMException && err.name === "AbortError";
        bail(aborted ? `asset not loaded within ${ASSET_TIMEOUT_MS / 1000} s` : `asset failed: ${String(err)}`);
        return;
      }
      clearTimeout(timer);
      if (disposed) return;

      try {
        const mesh = buildMesh(params, { maxTier: isPhone() ? 1 : 2 });
        renderer = new HeroRenderer(canvas, mesh, params.strutLength);
        renderer.setColors(readColors());
      } catch (err) {
        bail(err instanceof Error ? err.message : String(err));
        return;
      }

      const slot = document.getElementById(slotId);
      if (slot) slot.dataset.live = "1";
      layout();

      if (playIntro) {
        markSeen();
        beginIntro();
      } else {
        releaseArrivals();
        t = TL.done;
        backdrop.style.opacity = "0";
        setPhaseBoth("rest");
        schedule();
      }
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
      themeMedia.removeEventListener("change", onThemeChange);
      themeObserver.disconnect();
      io.disconnect();
      renderer?.dispose();
    };
  }, [mounted, slotId]);

  if (!mounted) return null;

  const intro = phase === "intro";
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
      <canvas ref={canvasRef} aria-hidden="true" />
      {NODE_IDS.map((id, i) => (
        <span
          key={id}
          ref={(el) => {
            labelRefs.current[i] = el;
          }}
          className="hero-label"
          style={{ opacity: 0 }}
          onPointerEnter={() => window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node: i } }))}
          onPointerLeave={() => window.dispatchEvent(new CustomEvent("hero-glow", { detail: { node: null } }))}
        >
          {id}
        </span>
      ))}
      {intro && (
        <div ref={hintRef} className="hero-hint" style={{ opacity: 0 }} aria-hidden="true">
          Press any key to skip
        </div>
      )}
    </div>,
    document.body,
  );
}
