// Builds the hero's committed assets from a seed: public/hero/lattice.json (the
// parametric lattice the client expands into vertex buffers) and public/hero/lattice.svg
// (the static fallback, same geometry and camera). Run with `pnpm build:lattice`.
import { mkdirSync, writeFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { buildLattice } from "../lib/hero/build.ts";
import { renderSvg, type SvgPalette } from "../lib/hero/svg.ts";
import { buildMesh } from "../lib/hero/geometry.ts";

const root = resolve(import.meta.dirname, "..");
const SEED = 7;
const SATELLITES = 12;

// The unmounted amethyst hero's own tones; the live stylesheet no longer carries them.
const PALETTE: SvgPalette = { shadow: "#1a0f2b", deep: "#3b1f5c", accent: "#8b5cf6", highlight: "#c4b5fd" };

const params = buildLattice({ seed: SEED, satellites: SATELLITES });
const outDir = resolve(root, "public/hero");
mkdirSync(outDir, { recursive: true });

const jsonPath = resolve(outDir, "lattice.json");
writeFileSync(jsonPath, JSON.stringify(params) + "\n");
const svgPath = resolve(outDir, "lattice.svg");
writeFileSync(svgPath, renderSvg(params, PALETTE));

const full = buildMesh(params, { maxTier: 2 });
const phone = buildMesh(params, { maxTier: 1 });
console.log(`lattice.json ${statSync(jsonPath).size} B, lattice.svg ${statSync(svgPath).size} B`);
console.log(`crystals ${params.crystals.length}, struts ${params.struts.length}, radius ${params.radius}`);
console.log(`triangles: desktop ${full.vertexCount / 3}, phone ${phone.vertexCount / 3}`);
