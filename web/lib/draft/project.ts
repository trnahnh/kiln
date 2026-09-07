import type { Vec2, Vec3 } from "./model.ts";

export interface Camera {
  yaw: number;
  pitch: number;
}

export const ISO: Camera = { yaw: Math.PI / 4, pitch: Math.PI / 6 };

// Orthographic projection after a yaw about Y and a pitch about X: the drafting
// convention, so parallel lines stay parallel and a box's hidden edges are decidable
// from its face normals alone.
export function project(p: Vec3, cam: Camera): Vec2 {
  const cy = Math.cos(cam.yaw);
  const sy = Math.sin(cam.yaw);
  const x1 = p[0] * cy - p[2] * sy;
  const z1 = p[0] * sy + p[2] * cy;
  const cp = Math.cos(cam.pitch);
  const sp = Math.sin(cam.pitch);
  const y2 = p[1] * cp - z1 * sp;
  return [x1, -y2];
}

export function viewDirection(cam: Camera): Vec3 {
  const cp = Math.cos(cam.pitch);
  const sp = Math.sin(cam.pitch);
  const cy = Math.cos(cam.yaw);
  const sy = Math.sin(cam.yaw);
  return [sy * cp, sp, cy * cp];
}

export function faceVisible(normal: Vec3, cam: Camera): boolean {
  const v = viewDirection(cam);
  return normal[0] * v[0] + normal[1] * v[1] + normal[2] * v[2] > 0;
}

export interface Bounds {
  minX: number;
  minY: number;
  maxX: number;
  maxY: number;
}

export function bounds(points: Vec2[]): Bounds {
  const b: Bounds = { minX: Infinity, minY: Infinity, maxX: -Infinity, maxY: -Infinity };
  for (const [x, y] of points) {
    if (x < b.minX) b.minX = x;
    if (y < b.minY) b.minY = y;
    if (x > b.maxX) b.maxX = x;
    if (y > b.maxY) b.maxY = y;
  }
  return b;
}

export interface Fit {
  scale: number;
  offsetX: number;
  offsetY: number;
}

// Contain-fit of the projected drawing into a rect, in pixels.
export function solveFit(b: Bounds, rect: { x: number; y: number; w: number; h: number }, margin = 0.9): Fit {
  const bw = b.maxX - b.minX || 1;
  const bh = b.maxY - b.minY || 1;
  const scale = Math.min((rect.w * margin) / bw, (rect.h * margin) / bh);
  const cx = (b.minX + b.maxX) / 2;
  const cy = (b.minY + b.maxY) / 2;
  return { scale, offsetX: rect.x + rect.w / 2 - cx * scale, offsetY: rect.y + rect.h / 2 - cy * scale };
}

export function toScreen(p: Vec2, fit: Fit): Vec2 {
  return [p[0] * fit.scale + fit.offsetX, p[1] * fit.scale + fit.offsetY];
}
