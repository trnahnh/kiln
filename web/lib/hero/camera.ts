import type { Vec3, Mat4 } from "./math.ts";
import { perspective, lookAt, mat4Multiply, clipShift, transformPoint } from "./math.ts";

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}
export interface Size {
  w: number;
  h: number;
}
export interface Fit {
  distance: number;
  shiftX: number;
  shiftY: number;
}

// Solves the camera distance so the bounding sphere's projected silhouette fits the rect,
// plus the lens shift that centres it there. A sphere of radius R at distance d subtends
// asin(R/d), which is wider than the R/d a naive fit would use: perspective magnifies.
export function solveFit(radius: number, fovY: number, viewport: Size, rect: Rect, margin = 0.92): Fit {
  const aspect = viewport.w / viewport.h;
  const tanHalf = Math.tan(fovY / 2);
  const ky = (rect.h / viewport.h) * margin;
  const kx = (rect.w / viewport.w) * margin;
  const tanTheta = Math.min(ky * tanHalf, kx * tanHalf * aspect);
  const theta = Math.atan(tanTheta);
  const distance = radius / Math.sin(theta);
  const cx = ((rect.x + rect.w / 2) / viewport.w) * 2 - 1;
  const cy = 1 - ((rect.y + rect.h / 2) / viewport.h) * 2;
  return { distance, shiftX: cx, shiftY: cy };
}

export function mixFit(a: Fit, b: Fit, t: number): Fit {
  return {
    distance: a.distance + (b.distance - a.distance) * t,
    shiftX: a.shiftX + (b.shiftX - a.shiftX) * t,
    shiftY: a.shiftY + (b.shiftY - a.shiftY) * t,
  };
}

export interface CameraInput {
  fovY: number;
  aspect: number;
  distance: number;
  yaw: number;
  pitch: number;
  shiftX: number;
  shiftY: number;
  radius: number;
}

export interface Camera {
  view: Mat4;
  proj: Mat4;
  eye: Vec3;
}

export function cameraMatrices(c: CameraInput): Camera {
  const eye: Vec3 = [
    c.distance * Math.cos(c.pitch) * Math.sin(c.yaw),
    c.distance * Math.sin(c.pitch),
    c.distance * Math.cos(c.pitch) * Math.cos(c.yaw),
  ];
  const view = lookAt(eye, [0, 0, 0], [0, 1, 0]);
  const near = Math.max(0.05, c.distance - c.radius * 4);
  const far = c.distance + c.radius * 4;
  const proj = mat4Multiply(clipShift(c.shiftX, c.shiftY), perspective(c.fovY, c.aspect, near, far));
  return { view, proj, eye };
}

export function projectToNdc(cam: Camera, p: Vec3): { x: number; y: number; depth: number } {
  const v = transformPoint(cam.view, p);
  const clip = transformPoint(cam.proj, [v[0], v[1], v[2]]);
  return { x: clip[0] / clip[3], y: clip[1] / clip[3], depth: -v[2] };
}
