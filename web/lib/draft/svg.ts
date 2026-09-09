import type { DraftModel, Vec2, Vec3 } from "./model.ts";
import { layoutDrawing } from "./layout.ts";
import { ISO, bounds, project, solveFit, toScreen, type Camera } from "./project.ts";

export interface SvgPalette {
  ground: string;
  line: string;
  faint: string;
  accent: string;
}

export interface SvgOptions {
  width?: number;
  height?: number;
  sheet?: string;
  annotate?: boolean;
}

interface AnnotLine {
  kind: "pipe" | "hidden" | "dim";
  points: Vec2[];
}

interface AnnotText {
  cls: "label" | "stack" | "value" | "measure";
  at: Vec2;
  dy: number;
  text: string;
}

interface Annotations {
  lines: AnnotLine[];
  texts: AnnotText[];
  hub: Vec2;
  extent: Vec2[];
}

const REJECT_RUN = 1.9;

const NOTES = [
  "1  POLICY IS EVALUATED BEFORE PROVISIONING. A DENIED CLAIM LEAVES NOTHING ON THE CLUSTER.",
  "2  NO SUBSYSTEM WRITES THE AUDIT TABLE. EVERY SUBSYSTEM PUBLISHES TO KAFKA; THE AUDIT SERVICE IS THE ONLY WRITER.",
  "3  DIMENSIONED VALUES ARE MEASURED IN THE RUN NAMED UNDER CHECKED. THEY ARE RESULTS, NOT TARGETS.",
];

function path(points: Vec2[]): string {
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join("");
}

// Where a ray from a block's centre toward `to` leaves its plan footprint, so a leader
// starts at the face instead of inside the wireframe.
function faceExit(centre: Vec2, to: Vec2, w: number, d: number): Vec2 {
  const dx = to[0] - centre[0];
  const dz = to[1] - centre[1];
  const t = Math.min(dx === 0 ? Infinity : w / 2 / Math.abs(dx), dz === 0 ? Infinity : d / 2 / Math.abs(dz));
  return [centre[0] + dx * t, centre[1] + dz * t];
}

// A solid triangle at the midpoint of a run's longest segment, so a static sheet says
// which way the request travels without the scene's animation to show it.
function chevron(points: Vec2[], size: number, fill: string): string {
  let at = 1;
  let longest = -1;
  for (let i = 1; i < points.length; i++) {
    const l = Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1]);
    if (l > longest) {
      longest = l;
      at = i;
    }
  }
  const [ax, ay] = points[at - 1];
  const [bx, by] = points[at];
  const angle = (Math.atan2(by - ay, bx - ax) * 180) / Math.PI;
  const d = `M${size.toFixed(1)} 0L${(-size * 0.72).toFixed(1)} ${(size * 0.6).toFixed(1)}L${(-size * 0.72).toFixed(1)} ${(-size * 0.6).toFixed(1)}Z`;
  return `<path d="${d}" fill="${fill}" transform="translate(${((ax + bx) / 2).toFixed(1)} ${((ay + by) / 2).toFixed(1)}) rotate(${angle.toFixed(1)})"/>`;
}

// The two paths an arrangement of the blocks alone cannot show: a claim the policy gate
// denies stops before anything is composed, and every subsystem reaches the audit table
// only by publishing to Kafka. Drawn from the same model, layout and camera as the rest.
function annotations(model: DraftModel, cam: Camera): Annotations {
  const layout = model.layouts.landscape;
  const { w, d, h } = layout.block;
  const P = (p: Vec3) => project(p, cam);
  const pipeY = h / 2;
  const busY = h * 0.3;

  const [gx, gz] = layout.positions[0];
  const stopX = gx - w / 2 - REJECT_RUN;
  const lines: AnnotLine[] = [
    { kind: "pipe", points: [P([gx - w / 2, pipeY, gz]), P([stopX, pipeY, gz])] },
    { kind: "dim", points: [P([stopX, pipeY - 0.5, gz]), P([stopX, pipeY + 0.5, gz])] },
  ];
  const denial = P([stopX, pipeY + 0.55, gz]);
  const texts: AnnotText[] = [
    { cls: "value", at: denial, dy: -14, text: "DENIED" },
    { cls: "measure", at: denial, dy: -2, text: "nothing composed" },
  ];

  const xs = layout.positions.map((p) => p[0]);
  const zs = layout.positions.map((p) => p[1]);
  const hubPlan: Vec2 = [(Math.min(...xs) + Math.max(...xs)) / 2, (Math.min(...zs) + Math.max(...zs)) / 2];
  const hub = P([hubPlan[0], busY, hubPlan[1]]);
  const sink = layout.positions[layout.positions.length - 1];
  for (const pos of layout.positions.slice(0, -1)) {
    const exit = faceExit(pos, hubPlan, w, d);
    lines.push({ kind: "hidden", points: [P([exit[0], busY, exit[1]]), hub] });
  }
  lines.push({
    kind: "pipe",
    points: [hub, P([sink[0], busY, hubPlan[1]]), P([sink[0], busY, sink[1] - d / 2])],
  });
  texts.push({ cls: "label", at: hub, dy: -18, text: "KAFKA" });
  texts.push({ cls: "stack", at: hub, dy: 22, text: "kiln.audit" });

  const extent = lines.flatMap((l) => l.points).concat(texts.map((t) => t.at));
  return { lines, texts, hub, extent };
}

// The finished drawing as a static sheet: the fallback when the scene cannot run, the
// source of the social card, and with `annotate` the README sheet. Same model, same
// projection, same fit rule.
export function renderSvg(model: DraftModel, palette: SvgPalette, opts: SvgOptions = {}): string {
  const { width = 1600, height = 1000, sheet = "1 OF 6", annotate = false } = opts;
  const drawing = layoutDrawing(model, "landscape", ISO);
  const extra = annotate ? annotations(model, ISO) : null;
  const inset = 28;
  const titleH = 92;
  const box = extra
    ? bounds([
        [drawing.bounds.minX, drawing.bounds.minY],
        [drawing.bounds.maxX, drawing.bounds.maxY],
        ...extra.extent,
      ])
    : drawing.bounds;
  const fit = solveFit(box, { x: inset + 40, y: inset + 30, w: width - inset * 2 - 80, h: height - inset * 2 - titleH - 60 }, 0.86);
  const S = (p: Vec2) => toScreen(p, fit);
  const k = Math.max(0.5, Math.min(1.1, fit.scale / 72));
  const parts: string[] = [];
  const cls: Record<string, string> = {
    grid: `stroke="${palette.faint}" stroke-width="1"`,
    edge: `stroke="${palette.line}" stroke-width="1.6"`,
    hidden: `stroke="${palette.line}" stroke-width="1" stroke-dasharray="6 5" opacity="0.55"`,
    pipe: `stroke="${palette.line}" stroke-width="2.4"`,
    dim: `stroke="${palette.accent}" stroke-width="1.2"`,
  };
  for (const p of drawing.prims) {
    if (p.points) {
      parts.push(`<path d="${path(p.points.map(S))}" fill="none" ${cls[p.kind]}/>`);
    } else if (p.at) {
      const [x, y] = S(p.at);
      if (p.kind === "label") parts.push(`<text x="${x.toFixed(1)}" y="${(y - 6 * k).toFixed(1)}" class="label">${p.text}</text>`);
      if (p.kind === "stack" && k >= 0.6) parts.push(`<text x="${x.toFixed(1)}" y="${(y + 4 * k).toFixed(1)}" class="stack">${p.text}</text>`);
      if (p.kind === "dimvalue") parts.push(`<text x="${x.toFixed(1)}" y="${(y - 14 * k).toFixed(1)}" class="value">${p.text}</text>`);
      if (p.kind === "dimlabel" && k >= 0.6) parts.push(`<text x="${x.toFixed(1)}" y="${(y - 2 * k).toFixed(1)}" class="measure">${p.text}</text>`);
    }
  }
  const end = S(drawing.travel[drawing.travel.length - 1]);
  parts.push(`<circle cx="${end[0].toFixed(1)}" cy="${end[1].toFixed(1)}" r="5" fill="${palette.accent}"/>`);

  if (extra) {
    for (const p of drawing.prims) {
      if (p.kind === "pipe" && p.points) parts.push(chevron(p.points.map(S), 9 * k, palette.line));
    }
    for (const l of extra.lines) {
      parts.push(`<path d="${path(l.points.map(S))}" fill="none" ${cls[l.kind]}/>`);
      if (l.kind === "pipe") parts.push(chevron(l.points.map(S), 9 * k, palette.line));
    }
    const [hx, hy] = S(extra.hub);
    parts.push(`<circle cx="${hx.toFixed(1)}" cy="${hy.toFixed(1)}" r="${(7 * k).toFixed(1)}" fill="${palette.ground}" stroke="${palette.line}" stroke-width="1.8"/>`);
    for (const t of extra.texts) {
      const [x, y] = S(t.at);
      parts.push(`<text x="${x.toFixed(1)}" y="${(y + t.dy * k).toFixed(1)}" class="${t.cls}">${t.text}</text>`);
    }
  }

  const tbW = 680;
  const tbX = width - inset - tbW;
  const tbY = height - inset - titleH;
  const titleBlock =
    `<g class="tb">` +
    `<rect x="${tbX}" y="${tbY}" width="${tbW}" height="${titleH}" fill="none" stroke="${palette.line}" stroke-width="1.4"/>` +
    `<line x1="${tbX}" y1="${tbY + 46}" x2="${tbX + tbW}" y2="${tbY + 46}" stroke="${palette.line}"/>` +
    `<line x1="${tbX + 160}" y1="${tbY}" x2="${tbX + 160}" y2="${tbY + titleH}" stroke="${palette.line}"/>` +
    `<line x1="${tbX + 340}" y1="${tbY + 46}" x2="${tbX + 340}" y2="${tbY + titleH}" stroke="${palette.line}"/>` +
    `<text x="${tbX + 12}" y="${tbY + 18}" class="tbk">PROJECT</text><text x="${tbX + 12}" y="${tbY + 38}" class="tbv big">KILN</text>` +
    `<text x="${tbX + 172}" y="${tbY + 18}" class="tbk">TITLE</text><text x="${tbX + 172}" y="${tbY + 38}" class="tbv">${model.title}, ${model.subtitle}</text>` +
    `<text x="${tbX + 12}" y="${tbY + 64}" class="tbk">SHEET</text><text x="${tbX + 12}" y="${tbY + 84}" class="tbv">${sheet}</text>` +
    `<text x="${tbX + 172}" y="${tbY + 64}" class="tbk">REV</text><text x="${tbX + 172}" y="${tbY + 84}" class="tbv">${model.rev}</text>` +
    `<text x="${tbX + 352}" y="${tbY + 64}" class="tbk">CHECKED</text><text x="${tbX + 352}" y="${tbY + 84}" class="tbv accent">${model.checked}</text>` +
    `</g>`;

  const notes = annotate
    ? `<g class="notes"><text x="${inset + 14}" y="${tbY + 12}" class="tbk">NOTES</text>` +
      NOTES.map((n, i) => `<text x="${inset + 14}" y="${tbY + 36 + i * 20}" class="note">${n}</text>`).join("") +
      `</g>`
    : "";

  const stampAt = annotate ? `${width - inset - 122} ${inset + 82}` : `${tbX - 150} ${tbY + 30}`;
  const stamp =
    `<g transform="translate(${stampAt}) rotate(-8)">` +
    `<rect x="-62" y="-20" width="124" height="40" fill="none" stroke="${palette.accent}" stroke-width="2.5"/>` +
    `<text x="0" y="8" class="stamp">CHECKED</text></g>`;

  const style =
    `text{font-family:"Big Shoulders Display","Archivo Narrow","Arial Narrow",sans-serif;fill:${palette.line};text-anchor:middle}` +
    `.label{font-size:${(22 * k).toFixed(1)}px;font-weight:700;letter-spacing:.06em}` +
    `.stack{font-family:"IBM Plex Mono",ui-monospace,monospace;font-size:${(11 * k).toFixed(1)}px;fill:${palette.line};opacity:.8}` +
    `.value{font-size:${(26 * k).toFixed(1)}px;font-weight:700;fill:${palette.accent}}` +
    `.measure{font-family:"IBM Plex Mono",ui-monospace,monospace;font-size:${(11 * k).toFixed(1)}px;fill:${palette.accent};opacity:.9}` +
    `.tbk{font-family:"IBM Plex Mono",ui-monospace,monospace;font-size:10px;text-anchor:start;opacity:.7}` +
    `.tbv{font-size:18px;font-weight:700;text-anchor:start;letter-spacing:.04em}.big{font-size:24px}.accent{fill:${palette.accent}}` +
    (annotate ? `.note{font-family:"IBM Plex Mono",ui-monospace,monospace;font-size:12px;text-anchor:start;opacity:.75}` : "") +
    `.stamp{font-size:22px;font-weight:800;letter-spacing:.12em;fill:${palette.accent}}`;

  const label = annotate
    ? "General arrangement of the kiln request flow: six subsystems on a drafted plane, each dimensioned with its measured result, with the policy gate's reject path and the Kafka stream every subsystem publishes to"
    : "General arrangement of the kiln request flow: six subsystems on a drafted plane, each dimensioned with its measured result";

  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="${label}">` +
    `<style>${style}</style>` +
    `<rect width="${width}" height="${height}" fill="${palette.ground}"/>` +
    `<rect x="${inset}" y="${inset}" width="${width - inset * 2}" height="${height - inset * 2}" fill="none" stroke="${palette.line}" stroke-width="1.4"/>` +
    parts.join("") +
    titleBlock +
    notes +
    stamp +
    `</svg>\n`
  );
}
