export type Window = [number, number];

// Every beat of the intro, as a pure function of time. Landscape drafts the whole sheet
// in view; portrait is a tracking shot, so its block, pipe and dimension windows are
// derived from when the request point reaches each block.
export interface Beats {
  tracking: boolean;
  borderStart: number;
  borderEnd: number;
  gridStart: number;
  gridEnd: number;
  block: (i: number) => Window;
  labelLag: number;
  pipe: (i: number) => Window;
  dim: (i: number) => Window;
  travelStart: number;
  travelEnd: number;
  pullbackStart: number;
  pullbackEnd: number;
  stampStart: number;
  stampEnd: number;
  handoffStart: number;
  handoffEnd: number;
  done: number;
}

export const SKIP_DURATION = 0.45;
export const ORBIT = { yaw: (4 * Math.PI) / 180, pitch: (3 * Math.PI) / 180 };
export const EDGE_STAGGER = 0.02;

export function landscapeBeats(): Beats {
  const blockStart = 1.0;
  const stagger = 0.3;
  const duration = 0.45;
  const block = (i: number): Window => [blockStart + i * stagger, blockStart + i * stagger + duration];
  return {
    tracking: false,
    borderStart: 0,
    borderEnd: 0.5,
    gridStart: 0.4,
    gridEnd: 1.2,
    block,
    labelLag: 0.3,
    pipe: (i) => {
      const [n0] = block(i + 1);
      return [n0 + 0.15, n0 + 0.45];
    },
    dim: (i) => [3.4 + i * 0.15, 3.7 + i * 0.15],
    travelStart: 3.0,
    travelEnd: 4.2,
    pullbackStart: 5.0,
    pullbackEnd: 5.0,
    stampStart: 4.6,
    stampEnd: 5.0,
    handoffStart: 5.0,
    handoffEnd: 5.5,
    done: 5.5,
  };
}

// `fractions[i]` is how far along the travel path block i's centre lies, 0 to 1.
export function portraitBeats(fractions: number[]): Beats {
  const travelStart = 1.0;
  const travelEnd = 5.0;
  const arrive = (i: number) => travelStart + fractions[i] * (travelEnd - travelStart);
  return {
    tracking: true,
    borderStart: 0,
    borderEnd: 0.5,
    gridStart: 0.3,
    gridEnd: 1.0,
    block: (i) => (i === 0 ? [0.45, 0.95] : [arrive(i) - 0.6, arrive(i) - 0.12]),
    labelLag: 0.25,
    pipe: (i) => [arrive(i) - 0.05, arrive(i) + 0.3],
    dim: (i) => [arrive(i), arrive(i) + 0.3],
    travelStart,
    travelEnd,
    pullbackStart: 5.0,
    pullbackEnd: 6.0,
    stampStart: 5.9,
    stampEnd: 6.2,
    handoffStart: 6.2,
    handoffEnd: 6.5,
    done: 6.5,
  };
}

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
export function warpedTime(t0: number, sinceSkip: number, done: number): number {
  return t0 + (done - t0) * easeOutCubic(sinceSkip / SKIP_DURATION);
}
