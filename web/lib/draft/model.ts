export type Vec3 = [number, number, number];
export type Vec2 = [number, number];

export interface Block {
  id: string;
  label: string;
  stack: string;
  value: string;
  measure: string;
}

export interface Layout {
  block: { w: number; d: number; h: number };
  positions: Vec2[];
}

export interface DraftModel {
  version: 1;
  blocks: Block[];
  layouts: { landscape: Layout; portrait: Layout };
  title: string;
  subtitle: string;
  rev: string;
  checked: string;
  checkedUrl: string;
}

export interface Edge {
  a: Vec3;
  b: Vec3;
  faces: number[];
}

export interface Box {
  center: Vec3;
  size: Vec3;
  corners: Vec3[];
  edges: Edge[];
  faceNormals: Vec3[];
}

// Corner order: bottom ring then top ring, each starting at (-x, -z) and going
// (+x,-z), (+x,+z), (-x,+z). Faces: 0 -y, 1 +y, 2 -z (front), 3 +x, 4 +z (back), 5 -x.
export function box(center: Vec3, size: Vec3): Box {
  const [cx, cy, cz] = center;
  const [w, h, d] = size;
  const x0 = cx - w / 2;
  const x1 = cx + w / 2;
  const z0 = cz - d / 2;
  const z1 = cz + d / 2;
  const y0 = cy;
  const y1 = cy + h;
  const corners: Vec3[] = [
    [x0, y0, z0], [x1, y0, z0], [x1, y0, z1], [x0, y0, z1],
    [x0, y1, z0], [x1, y1, z0], [x1, y1, z1], [x0, y1, z1],
  ];
  const faceNormals: Vec3[] = [[0, -1, 0], [0, 1, 0], [0, 0, -1], [1, 0, 0], [0, 0, 1], [-1, 0, 0]];
  const edge = (i: number, j: number, faces: number[]): Edge => ({ a: corners[i], b: corners[j], faces });
  const edges: Edge[] = [
    edge(0, 1, [0, 2]), edge(1, 2, [0, 3]), edge(2, 3, [0, 4]), edge(3, 0, [0, 5]),
    edge(4, 5, [1, 2]), edge(5, 6, [1, 3]), edge(6, 7, [1, 4]), edge(7, 4, [1, 5]),
    edge(0, 4, [2, 5]), edge(1, 5, [2, 3]), edge(2, 6, [3, 4]), edge(3, 7, [4, 5]),
  ];
  return { center, size, corners, edges, faceNormals };
}

// A Manhattan route on the ground plane from block i to block j, leaving and entering
// at the faces that look at each other. Aligned blocks get a straight segment.
export function route(from: Vec2, to: Vec2, w: number, d: number): Vec3[] {
  const [xi, zi] = from;
  const [xj, zj] = to;
  if (Math.abs(zi - zj) < 1e-6) {
    const s = Math.sign(xj - xi);
    return [[xi + (s * w) / 2, 0, zi], [xj - (s * w) / 2, 0, zj]];
  }
  if (Math.abs(xi - xj) < 1e-6) {
    const s = Math.sign(zj - zi);
    return [[xi, 0, zi + (s * d) / 2], [xj, 0, zj - (s * d) / 2]];
  }
  const s = Math.sign(zj - zi);
  const zm = (zi + zj) / 2;
  return [[xi, 0, zi + (s * d) / 2], [xi, 0, zm], [xj, 0, zm], [xj, 0, zj - (s * d) / 2]];
}

export function polylineLength(points: Vec2[]): number {
  let l = 0;
  for (let i = 1; i < points.length; i++) l += Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1]);
  return l;
}

export function pointAlong(points: Vec2[], fraction: number): Vec2 {
  const total = polylineLength(points);
  let target = Math.max(0, Math.min(1, fraction)) * total;
  for (let i = 1; i < points.length; i++) {
    const seg = Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1]);
    if (target <= seg || i === points.length - 1) {
      const f = seg === 0 ? 0 : target / seg;
      return [points[i - 1][0] + (points[i][0] - points[i - 1][0]) * f, points[i - 1][1] + (points[i][1] - points[i - 1][1]) * f];
    }
    target -= seg;
  }
  return points[points.length - 1];
}
