import type { Vec3 } from "./math.ts";
import { normalize, rotate, quatAxisAngle } from "./math.ts";

export const TL = {
  driftEnd: 0.6,
  firstLock: 1.1,
  lockStagger: 0.25,
  flight: 0.8,
  mainLead: 0.1,
  satelliteSpread: 0.15,
  strutStart: 2.45,
  strutStagger: 0.03,
  strutGrow: 0.35,
  sweepStart: 3.0,
  sweepEnd: 3.6,
  handoffStart: 3.6,
  handoffEnd: 4.2,
  done: 4.2,
  flash: 0.18,
  skipDuration: 0.45,
  scatterScale: 0.55,
  introYawSweep: (-24 * Math.PI) / 180,
  introDistanceScale: 1.3,
  sweepAngle: (-70 * Math.PI) / 180,
} as const;

export function clamp01(x: number): number {
  return x < 0 ? 0 : x > 1 ? 1 : x;
}
export function smoothstep(a: number, b: number, x: number): number {
  const t = clamp01((x - a) / (b - a));
  return t * t * (3 - 2 * t);
}
export function easeInOut(x: number): number {
  const t = clamp01(x);
  return t * t * (3 - 2 * t);
}
export function easeOutCubic(x: number): number {
  const t = clamp01(x);
  return 1 - Math.pow(1 - t, 3);
}

export function nodeLockAt(index: number): number {
  return TL.firstLock + index * TL.lockStagger;
}

export function warpedTime(t0: number, sinceSkip: number): number {
  return t0 + (TL.done - t0) * easeOutCubic(sinceSkip / TL.skipDuration);
}

const LIGHT_REST: Vec3 = normalize([-0.55, 0.75, 0.6]);

export function lightDirAt(t: number): Vec3 {
  const sweep = 1 - easeInOut(smoothstep(TL.sweepStart, TL.sweepEnd, t));
  return rotate(quatAxisAngle([0, 1, 0], TL.sweepAngle * sweep), LIGHT_REST);
}

export function cameraPoseAt(t: number): { yawOffset: number; distanceScale: number } {
  return {
    yawOffset: TL.introYawSweep * (1 - easeInOut(t / TL.sweepStart)),
    distanceScale: TL.introDistanceScale + (1 - TL.introDistanceScale) * easeInOut(t / 2.4),
  };
}

export function handoffAt(t: number): number {
  return easeInOut(smoothstep(TL.handoffStart, TL.handoffEnd, t));
}
