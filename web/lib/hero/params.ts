import type { Vec3 } from "./math.ts";

export const NODE_IDS = ["gitops", "operator", "scheduler", "delivery", "chaos", "audit"] as const;
export type NodeId = (typeof NODE_IDS)[number];

export const NODE_POSITIONS: Record<NodeId, Vec3> = {
  gitops: [0, -1, 0],
  operator: [0, 0, 1],
  scheduler: [1, 0, 0],
  delivery: [0, 0, -1],
  chaos: [-1, 0, 0],
  audit: [0, 1, 0],
};

export const CRYSTAL_STRIDE = 21;
export const C = {
  node: 0,
  restQ: 1,
  restP: 5,
  scatQ: 8,
  scatP: 12,
  radius: 15,
  height: 16,
  sides: 17,
  lockAt: 18,
  tier: 19,
  seed: 20,
} as const;

export interface LatticeNode {
  id: NodeId;
  position: Vec3;
  lockAt: number;
}

export interface Strut {
  a: number;
  b: number;
  lockAt: number;
}

export interface LatticeParams {
  version: 1;
  seed: number;
  nodes: LatticeNode[];
  crystals: number[][];
  struts: Strut[];
  strutRadius: number;
  strutLength: number;
  radius: number;
  camera: { fov: number; restYaw: number; restPitch: number };
}
