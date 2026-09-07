import { box, route, type DraftModel, type Vec2, type Vec3 } from "./model.ts";
import { project, faceVisible, bounds, type Camera, type Bounds } from "./project.ts";
import { DT, blockWindow, dimWindow } from "./timeline.ts";

export type PrimKind = "grid" | "edge" | "hidden" | "pipe" | "dim" | "dimvalue" | "dimlabel" | "label" | "stack";

export interface Prim {
  id: string;
  kind: PrimKind;
  points?: Vec2[];
  text?: string;
  at?: Vec2;
  t0: number;
  t1: number;
  block: number;
}

export interface Drawing {
  prims: Prim[];
  travel: Vec2[];
  bounds: Bounds;
  blockCentres: Vec2[];
}

const EDGE_STAGGER = 0.02;
const DIM_RISE = 1.15;
const GRID_PAD = 0.9;
const PORTRAIT_BAND = 3.4;

// Every primitive of the drawing in projected (pre-fit) coordinates, each with the
// window of the timeline during which it draws itself in. Pure: the same model, layout
// and camera always give the same drawing, which is what the fallback SVG relies on.
export function layoutDrawing(model: DraftModel, layoutName: "landscape" | "portrait", cam: Camera): Drawing {
  const layout = model.layouts[layoutName];
  const { w, d, h } = layout.block;
  const prims: Prim[] = [];
  const P = (p: Vec3) => project(p, cam);

  const xs = layout.positions.map((p) => p[0]);
  const zs = layout.positions.map((p) => p[1]);
  const gx0 = Math.floor(Math.min(...xs) - w / 2 - GRID_PAD);
  const gx1 = Math.ceil(Math.max(...xs) + w / 2 + GRID_PAD);
  const gz0 = Math.floor(Math.min(...zs) - d / 2 - GRID_PAD);
  const gz1 = Math.ceil(Math.max(...zs) + d / 2 + GRID_PAD);
  // The portrait layout runs along the plan diagonal, so its grid is a band about that
  // diagonal rather than the full square, which would dwarf the blocks.
  const band = layoutName === "portrait" ? PORTRAIT_BAND : Infinity;
  for (let x = gx0; x <= gx1; x++) {
    const z0 = Math.max(gz0, x - band);
    const z1 = Math.min(gz1, x + band);
    if (z1 > z0) prims.push({ id: `gx${x}`, kind: "grid", points: [P([x, 0, z0]), P([x, 0, z1])], t0: DT.gridStart, t1: DT.gridEnd, block: -1 });
  }
  for (let z = gz0; z <= gz1; z++) {
    const x0 = Math.max(gx0, z - band);
    const x1 = Math.min(gx1, z + band);
    if (x1 > x0) prims.push({ id: `gz${z}`, kind: "grid", points: [P([x0, 0, z]), P([x1, 0, z])], t0: DT.gridStart, t1: DT.gridEnd, block: -1 });
  }

  const centres: Vec2[] = [];
  model.blocks.forEach((b, i) => {
    const [x, z] = layout.positions[i];
    const bx = box([x, 0, z], [w, h, d]);
    const [t0, t1] = blockWindow(i);
    bx.edges.forEach((e, k) => {
      const visible = e.faces.some((f) => faceVisible(bx.faceNormals[f], cam));
      prims.push({
        id: `b${i}e${k}`,
        kind: visible ? "edge" : "hidden",
        points: [P(e.a), P(e.b)],
        t0: t0 + k * EDGE_STAGGER,
        t1: t1 + k * EDGE_STAGGER,
        block: i,
      });
    });
    const top: Vec3 = [x, h, z];
    centres.push(P(top));
    prims.push({ id: `b${i}label`, kind: "label", text: b.label, at: P(top), t0: t1, t1: t1 + DT.labelLag, block: i });
    prims.push({ id: `b${i}stack`, kind: "stack", text: b.stack, at: P([x, h / 2, z + d / 2]), t0: t1 + 0.1, t1: t1 + DT.labelLag + 0.1, block: i });

    const [d0, d1] = dimWindow(i);
    const c0: Vec3 = [x - w / 2, h, z + d / 2];
    const c1: Vec3 = [x + w / 2, h, z + d / 2];
    const e0: Vec3 = [c0[0], h + DIM_RISE, c0[2]];
    const e1: Vec3 = [c1[0], h + DIM_RISE, c1[2]];
    prims.push({ id: `d${i}x0`, kind: "dim", points: [P(c0), P(e0)], t0: d0, t1: d1, block: i });
    prims.push({ id: `d${i}x1`, kind: "dim", points: [P(c1), P(e1)], t0: d0, t1: d1, block: i });
    prims.push({ id: `d${i}line`, kind: "dim", points: [P(e0), P(e1)], t0: d0 + 0.05, t1: d1 + 0.05, block: i });
    const tick = 0.14;
    prims.push({ id: `d${i}k0`, kind: "dim", points: [P([e0[0] - tick, e0[1] - tick, e0[2]]), P([e0[0] + tick, e0[1] + tick, e0[2]])], t0: d1, t1: d1 + 0.1, block: i });
    prims.push({ id: `d${i}k1`, kind: "dim", points: [P([e1[0] - tick, e1[1] - tick, e1[2]]), P([e1[0] + tick, e1[1] + tick, e1[2]])], t0: d1, t1: d1 + 0.1, block: i });
    const mid: Vec3 = [x, h + DIM_RISE, z + d / 2];
    prims.push({ id: `d${i}value`, kind: "dimvalue", text: b.value, at: P(mid), t0: d1, t1: d1 + 0.2, block: i });
    prims.push({ id: `d${i}measure`, kind: "dimlabel", text: b.measure, at: P(mid), t0: d1 + 0.05, t1: d1 + 0.25, block: i });
  });

  const travel: Vec2[] = [];
  for (let i = 0; i < model.blocks.length; i++) {
    const [x, z] = layout.positions[i];
    travel.push(P([x, h / 2, z]));
    if (i === model.blocks.length - 1) break;
    const pts = route(layout.positions[i], layout.positions[i + 1], w, d).map((p): Vec3 => [p[0], h / 2, p[2]]);
    const [n0] = blockWindow(i + 1);
    prims.push({
      id: `p${i}`,
      kind: "pipe",
      points: pts.map(P),
      t0: n0 + DT.pipeLag,
      t1: n0 + DT.pipeLag + DT.pipeDuration,
      block: -1,
    });
    for (const p of pts) travel.push(P(p));
  }

  // The fit is solved on the blocks, pipes and dimensions; the construction grid may run past it.
  const all: Vec2[] = [];
  for (const p of prims) {
    if (p.kind === "grid") continue;
    if (p.points) all.push(...p.points);
    if (p.at) all.push(p.at);
  }
  return { prims, travel, bounds: bounds(all), blockCentres: centres };
}
