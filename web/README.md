# kiln landing page

The site for [kiln](../README.md): a 3-D hero (a scattered field of faceted shards that
crystallizes into a six-node lattice, one node per subsystem), the six subsystems with the
numbers from the validation week, the structural invariants with their decision records, and
the validation table verbatim. Next.js App Router, React, Tailwind, TypeScript, no other
runtime dependencies. Deployed from this folder (Vercel root directory `web/`), production
from `origin/main`.

## Run

```
pnpm install
pnpm dev            # http://localhost:3000
pnpm build && pnpm start
```

## Where the numbers come from

`docs/METRICS.md` in the repo root is the only owner of every figure on the page.

```
pnpm pull:metrics   # parse docs/METRICS.md into content/metrics.json
pnpm check:metrics  # exit 1 if the committed JSON would change
```

CI runs the check on every push to `main`, so a doc edit that moves a number fails the build
until `content/metrics.json` is regenerated and committed.

## The hero asset

```
pnpm build:lattice  # writes public/hero/lattice.json and public/hero/lattice.svg
```

`scripts/build-lattice.ts` builds the lattice from a seed: six geode clusters at the vertices
of an octahedron, twelve struts, every crystal's rest and scattered pose, and each one's lock
time on the timeline. The JSON is parametric (about 11 KB); the client expands it into vertex
buffers. The SVG is the finished lattice drawn from the same camera with the same tone rules,
and it is what the hero slot shows when the scene cannot run. CI rebuilds both and fails if
they differ from what is committed.

The scene itself is `lib/hero/`: `geometry.ts` (prisms, struts, the vertex layout),
`camera.ts` (perspective, and a contain-fit that solves for the projected silhouette),
`timeline.ts` (every beat as a function of `t`), `shaders.ts` and `renderer.ts` (one WebGL
program), `gate.ts` and `gate-script.ts` (the play-once gating; both apply the same test).

## Gating

The intro plays once per session. The inline script at the top of `<body>` decides before
first paint whether it will play and, if so, pauses the page's arrival animations until the
scene hands off. A hard reload (`Ctrl+Shift+R`) plays it again; a normal reload does not.
Every bail-out logs `[hero] skipped: <reason>` and leaves the session flag alone:

| reason | when |
|---|---|
| `already seen this session` | the flag is set and this is not a hard reload |
| `prefers-reduced-motion is set` | the OS preference; the page arrives with the static SVG |
| `no WebGL context` | WebGL unavailable or disabled |
| `shader failed to link: ...` | the program did not compile or link |
| `asset not loaded within 2.5 s` | `lattice.json` did not arrive in time |

Any input skips: pointer, key, wheel, touch. A skip is a time-warp, not a cut: whatever
remains of the timeline finishes in about 450 ms, then the normal hand-off runs. The
"Replay intro" control appears once the flag is set and dispatches `replay-intro`.

## Verification

Filmed headless in real time over the DevTools protocol (never with a virtual time budget,
which does not render WebGL frames). Uses Playwright's Chromium if present, else Chrome;
set `CHROME_PATH` to point at another binary.

```
pnpm build && PORT=3100 pnpm start

node scripts/film.mjs --url http://localhost:3100 --out ../.film/desktop --width 1920 --height 1080
node scripts/film.mjs --url http://localhost:3100 --out ../.film/skip --width 1920 --height 1080 --skip-at 1500 --frames 1450,1700,2000,2400
node scripts/film.mjs --url http://localhost:3100 --out ../.film/phone --width 393 --height 852 --mobile --frames 2000,4600,5300 --scrolls 5000:700
node scripts/gating.mjs --url http://localhost:3100
```

`film.mjs` writes numbered frames plus `console.txt` (every console line and the final gating
state). `gating.mjs` runs the matrix: first open, normal reload, hard reload, route change and
back, a new tab, reduced motion, and a `--disable-3d-apis` run, checking the flag, the stage
phase and the logged reason for each, and exits non-zero on any failure.

The social card is the `/og` route captured at 1200x630:

```
pnpm og
```
