export const DT = {
  borderStart: 0.0,
  borderEnd: 0.5,
  gridStart: 0.4,
  gridEnd: 1.2,
  blockStart: 1.0,
  blockStagger: 0.3,
  blockDuration: 0.45,
  labelLag: 0.3,
  pipeLag: 0.15,
  pipeDuration: 0.3,
  busStart: 2.9,
  busEnd: 3.4,
  travelStart: 3.0,
  travelEnd: 4.2,
  dimStart: 3.4,
  dimStagger: 0.15,
  dimDuration: 0.3,
  stampStart: 4.6,
  stampEnd: 5.0,
  handoffStart: 5.0,
  handoffEnd: 5.5,
  done: 5.5,
  skipDuration: 0.45,
  orbitYaw: (4 * Math.PI) / 180,
  orbitPitch: (3 * Math.PI) / 180,
} as const;

export function clamp01(x: number): number {
  return x < 0 ? 0 : x > 1 ? 1 : x;
}
export function smoothstep(a: number, b: number, x: number): number {
  const t = clamp01((x - a) / (b - a));
  return t * t * (3 - 2 * t);
}
export function easeOutCubic(x: number): number {
  const t = clamp01(x);
  return 1 - Math.pow(1 - t, 3);
}
export function linear(a: number, b: number, x: number): number {
  return clamp01((x - a) / (b - a));
}
export function warpedTime(t0: number, sinceSkip: number): number {
  return t0 + (DT.done - t0) * easeOutCubic(sinceSkip / DT.skipDuration);
}
export function handoffAt(t: number): number {
  return smoothstep(DT.handoffStart, DT.handoffEnd, t);
}
export function blockWindow(i: number): [number, number] {
  const s = DT.blockStart + i * DT.blockStagger;
  return [s, s + DT.blockDuration];
}
export function dimWindow(i: number): [number, number] {
  const s = DT.dimStart + i * DT.dimStagger;
  return [s, s + DT.dimDuration];
}
