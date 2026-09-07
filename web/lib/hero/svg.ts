import type { Vec3 } from "./math.ts";
import { sub, dot, normalize, scale, length } from "./math.ts";
import type { LatticeParams } from "./params.ts";
import { buildMesh, restTriangles } from "./geometry.ts";
import { solveFit, cameraMatrices, projectToNdc } from "./camera.ts";
import { lightDirAt, TL } from "./timeline.ts";
import { toneFor, SHADE } from "./shading.ts";

export interface SvgPalette {
  shadow: string;
  deep: string;
  accent: string;
  highlight: string;
}

// The static fallback: the finished lattice, same geometry, same camera, same tone
// rules as the shader, drawn painter's-order with back faces culled.
export function renderSvg(params: LatticeParams, palette: SvgPalette, size = 1000): string {
  const mesh = buildMesh(params, { maxTier: 2 });
  const tris = restTriangles(mesh);
  const fit = solveFit(params.radius, params.camera.fov, { w: size, h: size }, { x: 0, y: 0, w: size, h: size });
  const cam = cameraMatrices({
    fovY: params.camera.fov,
    aspect: 1,
    distance: fit.distance,
    yaw: params.camera.restYaw,
    pitch: params.camera.restPitch,
    shiftX: 0,
    shiftY: 0,
    radius: params.radius,
  });
  const light = lightDirAt(TL.done);

  const faces = tris
    .map((t) => {
      const centroid: Vec3 = scale([t.a[0] + t.b[0] + t.c[0], t.a[1] + t.b[1] + t.c[1], t.a[2] + t.b[2] + t.c[2]], 1 / 3);
      const toEye = sub(cam.eye, centroid);
      if (dot(t.normal, toEye) <= 0) return null;
      const v = normalize(toEye);
      const lambert = dot(t.normal, light);
      const r = sub(scale(t.normal, 2 * dot(light, t.normal)), light);
      const spec = Math.pow(Math.max(dot(r, v), 0), SHADE.specularPower);
      const tone = toneFor(lambert, spec, true);
      const pts = [t.a, t.b, t.c].map((p) => {
        const n = projectToNdc(cam, p);
        return [((n.x + 1) / 2) * size, ((1 - n.y) / 2) * size];
      });
      return { depth: length(toEye), tone, pts };
    })
    .filter((f): f is NonNullable<typeof f> => f !== null)
    .sort((a, b) => b.depth - a.depth);

  const cls = ["s", "d", "a", "h"];
  const body = faces
    .map((f) => `<polygon class="${cls[f.tone]}" points="${f.pts.map((p) => p.map((x) => x.toFixed(1)).join(",")).join(" ")}"/>`)
    .join("");
  const style =
    `.s{fill:var(--facet-shadow,${palette.shadow})}` +
    `.d{fill:var(--facet-deep,${palette.deep})}` +
    `.a{fill:var(--facet-accent,${palette.accent})}` +
    `.h{fill:var(--facet-highlight,${palette.highlight})}`;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" role="img" aria-label="The kiln lattice: six crystal clusters at the vertices of an octahedron">` +
    `<style>${style}</style>${body}</svg>\n`
  );
}

