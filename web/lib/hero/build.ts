import type { Vec3, Quat } from "./math.ts";
import {
  normalize,
  cross,
  add,
  scale,
  dot,
  anyPerpendicular,
  quatFromUnitVectors,
  quatAxisAngle,
  quatMul,
  quatDot,
} from "./math.ts";
import { NODE_IDS, NODE_POSITIONS, type LatticeParams, type Strut } from "./params.ts";
import { TL, nodeLockAt } from "./timeline.ts";
import { buildMesh, boundingRadius } from "./geometry.ts";

export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export interface BuildOptions {
  seed: number;
  satellites: number;
}

const ESTIMATED_RADIUS = 1.45;

function round(x: number): number {
  return Math.round(x * 10000) / 10000;
}

function randomDirection(rng: () => number): Vec3 {
  const z = 2 * rng() - 1;
  const phi = 2 * Math.PI * rng();
  const r = Math.sqrt(1 - z * z);
  return [r * Math.cos(phi), z, r * Math.sin(phi)];
}

function randomRotation(rng: () => number): Quat {
  const u1 = rng();
  const u2 = 2 * Math.PI * rng();
  const u3 = 2 * Math.PI * rng();
  const a = Math.sqrt(1 - u1);
  const b = Math.sqrt(u1);
  return [a * Math.sin(u2), a * Math.cos(u2), b * Math.sin(u3), b * Math.cos(u3)];
}

function restRotation(axis: Vec3, roll: number): Quat {
  return quatMul(quatAxisAngle(axis, roll), quatFromUnitVectors([0, 1, 0], axis));
}

function tangentOf(outward: Vec3, rng: () => number): Vec3 {
  const t = cross(outward, randomDirection(rng));
  const l = Math.hypot(t[0], t[1], t[2]);
  return l < 1e-4 ? anyPerpendicular(outward) : scale(t, 1 / l);
}

function crystal(
  rng: () => number,
  node: number,
  axis: Vec3,
  base: Vec3,
  radius: number,
  height: number,
  sides: number,
  lockAt: number,
  tier: number,
): number[] {
  const restQ = restRotation(axis, rng() * Math.PI * 2);
  let scatQ = randomRotation(rng);
  if (quatDot(scatQ, restQ) < 0) scatQ = scatQ.map((x) => -x) as Quat;
  const scatP = scale(randomDirection(rng), ESTIMATED_RADIUS * (1.6 + rng()));
  return [
    node,
    ...restQ.map(round),
    ...base.map(round),
    ...scatQ.map(round),
    ...scatP.map(round),
    round(radius),
    round(height),
    sides,
    round(lockAt),
    tier,
    round(rng()),
  ];
}

export function buildLattice(opts: BuildOptions): LatticeParams {
  const rng = mulberry32(opts.seed);
  const nodes = NODE_IDS.map((id, i) => ({ id, position: NODE_POSITIONS[id], lockAt: nodeLockAt(i) }));
  const crystals: number[][] = [];

  nodes.forEach((n, i) => {
    const outward = normalize(n.position);
    const mainAxis = normalize(add(outward, scale(tangentOf(outward, rng), 0.12)));
    crystals.push(
      crystal(
        rng,
        i,
        mainAxis,
        add(n.position, scale(outward, -0.05)),
        0.12 + (rng() - 0.5) * 0.04,
        0.5 + (rng() - 0.5) * 0.08,
        5 + Math.floor(rng() * 3),
        n.lockAt - TL.mainLead,
        0,
      ),
    );
    for (let j = 0; j < opts.satellites; j++) {
      const u = 0.3 + rng() * 0.7;
      const axis = normalize(add(scale(outward, u), scale(tangentOf(outward, rng), (1 - u) * 1.2)));
      const base = add(add(n.position, scale(randomDirection(rng), rng() * 0.06)), scale(outward, -0.02));
      crystals.push(
        crystal(
          rng,
          i,
          axis,
          base,
          0.04 + rng() * 0.035,
          0.2 + rng() * 0.16,
          4 + Math.floor(rng() * 3),
          n.lockAt + rng() * TL.satelliteSpread,
          j < 6 ? 1 : 2,
        ),
      );
    }
  });

  const edges: [number, number][] = [];
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      if (Math.abs(dot(nodes[i].position, nodes[j].position)) < 1e-9) edges.push([i, j]);
    }
  }
  for (let i = edges.length - 1; i > 0; i--) {
    const k = Math.floor(rng() * (i + 1));
    [edges[i], edges[k]] = [edges[k], edges[i]];
  }
  const struts: Strut[] = edges.map(([a, b], k) => ({ a, b, lockAt: round(TL.strutStart + k * TL.strutStagger) }));

  const params: LatticeParams = {
    version: 1,
    seed: opts.seed,
    nodes,
    crystals,
    struts,
    strutRadius: 0.018,
    strutLength: round(Math.SQRT2),
    radius: 0,
    camera: { fov: (32 * Math.PI) / 180, restYaw: (18 * Math.PI) / 180, restPitch: (12 * Math.PI) / 180 },
  };
  params.radius = round(boundingRadius(buildMesh(params, { maxTier: 2 })));
  return params;
}
