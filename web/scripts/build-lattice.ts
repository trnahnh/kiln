// Builds the hero's committed assets from a seed: public/hero/lattice.json (the
// parametric lattice the client expands into vertex buffers) and public/hero/lattice.svg
// (the static fallback, same geometry and camera). Run with `pnpm build:lattice`.
import { mkdirSync, readFileSync, writeFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { buildLattice } from "../lib/hero/build.ts";
import { renderSvg, type SvgPalette } from "../lib/hero/svg.ts";
import { buildMesh } from "../lib/hero/geometry.ts";

const root = resolve(import.meta.dirname, "..");
const SEED = 7;
const SATELLITES = 12;

function paletteFromCss(): SvgPalette {
  const css = readFileSync(resolve(root, "app/globals.css"), "utf8");
  const read = (name: string): string => {
    const m = css.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`));
    if (!m) throw new Error(`app/globals.css does not define --${name}`);
    return m[1];
  };
  return {
    shadow: read("facet-shadow"),
    deep: read("facet-deep"),
    accent: read("facet-accent"),
    highlight: read("facet-highlight"),
  };
}

const params = buildLattice({ seed: SEED, satellites: SATELLITES });
const outDir = resolve(root, "public/hero");
mkdirSync(outDir, { recursive: true });

const jsonPath = resolve(outDir, "lattice.json");
writeFileSync(jsonPath, JSON.stringify(params) + "\n");
const svgPath = resolve(outDir, "lattice.svg");
writeFileSync(svgPath, renderSvg(params, paletteFromCss()));

const full = buildMesh(params, { maxTier: 2 });
const phone = buildMesh(params, { maxTier: 1 });
console.log(`lattice.json ${statSync(jsonPath).size} B, lattice.svg ${statSync(svgPath).size} B`);
console.log(`crystals ${params.crystals.length}, struts ${params.struts.length}, radius ${params.radius}`);
console.log(`triangles: desktop ${full.vertexCount / 3}, phone ${phone.vertexCount / 3}`);
