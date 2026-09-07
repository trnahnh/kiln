import type { Vec3, Quat } from "./math.ts";
import { sub, cross, normalize, dot, rotate, add } from "./math.ts";
import { C, CRYSTAL_STRIDE, type LatticeParams } from "./params.ts";

export const FLOATS_PER_VERTEX = 24;
export const KIND_CRYSTAL = 0;
export const KIND_STRUT = 1;

export interface Mesh {
  data: Float32Array;
  vertexCount: number;
}

interface Tri {
  a: Vec3;
  b: Vec3;
  c: Vec3;
}

function orient(t: Tri, inside: Vec3): Tri {
  const n = cross(sub(t.b, t.a), sub(t.c, t.a));
  const centroid: Vec3 = [
    (t.a[0] + t.b[0] + t.c[0]) / 3,
    (t.a[1] + t.b[1] + t.c[1]) / 3,
    (t.a[2] + t.b[2] + t.c[2]) / 3,
  ];
  return dot(n, sub(centroid, inside)) < 0 ? { a: t.a, b: t.c, c: t.b } : t;
}

export function prismTriangles(radius: number, height: number, sides: number, phase: number): Tri[] {
  const body = height * 0.72;
  const ring = (y: number): Vec3[] =>
    Array.from({ length: sides }, (_, k) => {
      const th = phase + (2 * Math.PI * k) / sides;
      return [radius * Math.cos(th), y, radius * Math.sin(th)];
    });
  const lo = ring(0);
  const hi = ring(body);
  const tip: Vec3 = [0, height, 0];
  const base: Vec3 = [0, 0, 0];
  const inside: Vec3 = [0, height / 2, 0];
  const tris: Tri[] = [];
  for (let k = 0; k < sides; k++) {
    const k2 = (k + 1) % sides;
    tris.push(orient({ a: lo[k], b: lo[k2], c: hi[k2] }, inside));
    tris.push(orient({ a: lo[k], b: hi[k2], c: hi[k] }, inside));
    tris.push(orient({ a: hi[k], b: hi[k2], c: tip }, inside));
    tris.push(orient({ a: base, b: lo[k2], c: lo[k] }, inside));
  }
  return tris;
}

export function strutTriangles(radius: number, len: number, sides: number): Tri[] {
  const ring = (x: number): Vec3[] =>
    Array.from({ length: sides }, (_, k) => {
      const th = (2 * Math.PI * k) / sides;
      return [x, radius * Math.cos(th), radius * Math.sin(th)];
    });
  const a = ring(0);
  const b = ring(len);
  const inside: Vec3 = [len / 2, 0, 0];
  const tris: Tri[] = [];
  for (let k = 0; k < sides; k++) {
    const k2 = (k + 1) % sides;
    tris.push(orient({ a: a[k], b: a[k2], c: b[k2] }, inside));
    tris.push(orient({ a: a[k], b: b[k2], c: b[k] }, inside));
    tris.push(orient({ a: [0, 0, 0], b: a[k2], c: a[k] }, inside));
    tris.push(orient({ a: [len, 0, 0], b: b[k], c: b[k2] }, inside));
  }
  return tris;
}

function faceNormal(t: Tri): Vec3 {
  return normalize(cross(sub(t.b, t.a), sub(t.c, t.a)));
}

function quatFromX(dir: Vec3): Quat {
  const x: Vec3 = [1, 0, 0];
  const d = dot(x, dir);
  if (d < -0.999999) return [0, 1, 0, 0];
  const c = cross(x, dir);
  const q: Quat = [c[0], c[1], c[2], 1 + d];
  const l = Math.hypot(q[0], q[1], q[2], q[3]);
  return [q[0] / l, q[1] / l, q[2] / l, q[3] / l];
}

export interface MeshOptions {
  maxTier: number;
}

export function buildMesh(params: LatticeParams, opts: MeshOptions): Mesh {
  const chunks: number[] = [];
  const push = (
    local: Vec3,
    normal: Vec3,
    restQ: Quat,
    restP: Vec3,
    scatQ: Quat,
    scatP: Vec3,
    aux: [number, number, number, number],
  ) => {
    chunks.push(...local, ...normal, ...restQ, ...restP, ...scatQ, ...scatP, ...aux);
  };

  for (const c of params.crystals) {
    if (c.length !== CRYSTAL_STRIDE) {
      throw new Error("crystal has " + c.length + " fields, expected " + CRYSTAL_STRIDE);
    }
    if (c[C.tier] > opts.maxTier) continue;
    const restQ = c.slice(C.restQ, C.restQ + 4) as Quat;
    const restP = c.slice(C.restP, C.restP + 3) as Vec3;
    const scatQ = c.slice(C.scatQ, C.scatQ + 4) as Quat;
    const scatP = c.slice(C.scatP, C.scatP + 3) as Vec3;
    const aux: [number, number, number, number] = [KIND_CRYSTAL, c[C.lockAt], c[C.node], c[C.seed]];
    const tris = prismTriangles(c[C.radius], c[C.height], c[C.sides], c[C.seed] * Math.PI * 2);
    for (const t of tris) {
      const n = faceNormal(t);
      push(t.a, n, restQ, restP, scatQ, scatP, aux);
      push(t.b, n, restQ, restP, scatQ, scatP, aux);
      push(t.c, n, restQ, restP, scatQ, scatP, aux);
    }
  }

  for (const s of params.struts) {
    const pa = params.nodes[s.a].position;
    const pb = params.nodes[s.b].position;
    const restQ = quatFromX(normalize(sub(pb, pa)));
    const aux: [number, number, number, number] = [KIND_STRUT, s.lockAt, -1, 0];
    for (const t of strutTriangles(params.strutRadius, params.strutLength, 6)) {
      const n = faceNormal(t);
      push(t.a, n, restQ, pa, restQ, pa, aux);
      push(t.b, n, restQ, pa, restQ, pa, aux);
      push(t.c, n, restQ, pa, restQ, pa, aux);
    }
  }

  return { data: new Float32Array(chunks), vertexCount: chunks.length / FLOATS_PER_VERTEX };
}

export interface RestTriangle {
  a: Vec3;
  b: Vec3;
  c: Vec3;
  normal: Vec3;
  node: number;
}

export function restTriangles(mesh: Mesh): RestTriangle[] {
  const out: RestTriangle[] = [];
  const d = mesh.data;
  const S = FLOATS_PER_VERTEX;
  const world = (i: number): Vec3 => {
    const o = i * S;
    const local: Vec3 = [d[o], d[o + 1], d[o + 2]];
    const q: Quat = [d[o + 6], d[o + 7], d[o + 8], d[o + 9]];
    const p: Vec3 = [d[o + 10], d[o + 11], d[o + 12]];
    return add(rotate(q, local), p);
  };
  for (let i = 0; i < mesh.vertexCount; i += 3) {
    const o = i * S;
    const q: Quat = [d[o + 6], d[o + 7], d[o + 8], d[o + 9]];
    const n = rotate(q, [d[o + 3], d[o + 4], d[o + 5]]);
    out.push({ a: world(i), b: world(i + 1), c: world(i + 2), normal: n, node: d[o + 22] });
  }
  return out;
}

export function boundingRadius(mesh: Mesh): number {
  let r = 0;
  for (const t of restTriangles(mesh)) {
    for (const p of [t.a, t.b, t.c]) r = Math.max(r, Math.hypot(p[0], p[1], p[2]));
  }
  return r;
}
