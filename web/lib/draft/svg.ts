import type { DraftModel, Vec2 } from "./model.ts";
import { layoutDrawing } from "./layout.ts";
import { ISO, solveFit, toScreen } from "./project.ts";

export interface SvgPalette {
  ground: string;
  line: string;
  faint: string;
  accent: string;
}

function path(points: Vec2[]): string {
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join("");
}

// The finished drawing as a static sheet: the fallback when the scene cannot run, and
// the source of the social card. Same model, same projection, same fit rule.
export function renderSvg(model: DraftModel, palette: SvgPalette, width = 1600, height = 1000): string {
  const drawing = layoutDrawing(model, "landscape", ISO);
  const inset = 28;
  const titleH = 92;
  const fit = solveFit(drawing.bounds, { x: inset + 40, y: inset + 30, w: width - inset * 2 - 80, h: height - inset * 2 - titleH - 60 }, 0.86);
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
    `<text x="${tbX + 12}" y="${tbY + 64}" class="tbk">SHEET</text><text x="${tbX + 12}" y="${tbY + 84}" class="tbv">1 OF 6</text>` +
    `<text x="${tbX + 172}" y="${tbY + 64}" class="tbk">REV</text><text x="${tbX + 172}" y="${tbY + 84}" class="tbv">${model.rev}</text>` +
    `<text x="${tbX + 352}" y="${tbY + 64}" class="tbk">CHECKED</text><text x="${tbX + 352}" y="${tbY + 84}" class="tbv accent">${model.checked}</text>` +
    `</g>`;

  const stamp =
    `<g transform="translate(${tbX - 150} ${tbY + 30}) rotate(-8)">` +
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
    `.stamp{font-size:22px;font-weight:800;letter-spacing:.12em;fill:${palette.accent}}`;

  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="General arrangement of the kiln request flow: six subsystems on a drafted plane, each dimensioned with its measured result">` +
    `<style>${style}</style>` +
    `<rect width="${width}" height="${height}" fill="${palette.ground}"/>` +
    `<rect x="${inset}" y="${inset}" width="${width - inset * 2}" height="${height - inset * 2}" fill="none" stroke="${palette.line}" stroke-width="1.4"/>` +
    parts.join("") +
    titleBlock +
    stamp +
    `</svg>\n`
  );
}
