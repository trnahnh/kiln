# kiln landing page

The site for [kiln](../README.md), set as a cyanotype technical drawing: sheet 1 is a general
arrangement of the request flow that drafts itself in front of you (six wireframe blocks on a
drafted plane, dimensioned with the validation week's real numbers, checked against the CI run),
and every section below is a numbered sheet with its own title block. Next.js App Router, React, Tailwind, TypeScript, no other
runtime dependencies. Deployed from this folder (Vercel root directory `web/`), production
from `origin/main`, live at https://kiln-idp.vercel.app. Its checks run in
`.github/workflows/web.yaml` on pushes that touch `web/`, `docs/METRICS.md` or the workflow; the
platform pipeline ignores web-only pushes.

## Run

```
pnpm install
pnpm dev            # http://localhost:3000
pnpm build && pnpm start
```

## What is on the page

Sheet 1 is the drawing. Sheets 2 to 6: a specimen viewer per subsystem (stack, the
problems it solves, the real CRD or contract it owns from `docs/API_REFERENCE.md`, its decision
record, its validation number), three working demos of the platform's own rules (the audit hash
chain with SHA-256 computed in the browser by the rule in `docs/DATA_MODEL.md`, the CostAware
placement score, the chaos agent's blast-radius floor), the three injected failures drawn to scale,
the invariants with their ADRs, and the validation table verbatim.

## Where the numbers come from

`docs/METRICS.md` in the repo root is the only owner of every figure on the page.

```
pnpm pull:metrics   # parse docs/METRICS.md into content/metrics.json
pnpm check:metrics  # exit 1 if the committed JSON would change
```

CI runs the check on every push to `main`, so a doc edit that moves a number fails the build
until `content/metrics.json` is regenerated and committed.

## The drawing

```
pnpm build:draft    # writes public/draft/model.json, drawing.svg and card.svg,
                    # and ../docs/assets/architecture.svg
```

`scripts/build-draft.ts` builds the model (six blocks, two layouts, the measured values stamped
from `content/metrics.json`) and renders the finished sheet as the fallback SVG and the social
card. It renders it once more with `annotate` for the root [README](../README.md#architecture):
the same sheet plus the reject branch off the policy gate, the Kafka hub every subsystem
publishes to, a direction chevron on each run (a static sheet has no animation to show which way
the request travels) and a notes block. CI diffs `docs/assets` with `public/draft`. `lib/draft/` holds the line model (`model.ts`), the orthographic projection with hidden-line
detection and the contain-fit (`project.ts`), every primitive with its draw-in window
(`layout.ts`), the beats (`timeline.ts`) and the static renderer (`svg.ts`). The client
(`components/DraftScene.tsx`) projects the model each frame into inline SVG, so the drawing
draws itself in with stroke animation, orbits under the pointer, and stays crisp at any DPR
without WebGL. Text is sized from the fit scale so the small rest and phone drawings stay legible.
On phones the intro is a tracking shot: the camera follows the request point down the column and
every block's beats are derived from when the request reaches it (`portraitBeats`), then the view
pulls back to the rest fit. The page is fluid: the root font size scales with the viewport and the
sheets span it, so a 1440p screen gets a 1440p sheet.

## The earlier hero

The amethyst crystal lattice (`components/HeroScene.tsx`, `lib/hero/`, `public/hero/`) is kept in
the repo unmounted.

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
| `asset not loaded within 2.5 s` | `model.json` did not arrive in time |

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
back, a new tab, reduced motion, and a `--disable-3d-apis` run (which must still play, since the
drawing is SVG), checking the flag, the stage phase and the logged reason for each, and exits
non-zero on any failure.

The social card is the `/og` route captured at 1200x630:

```
pnpm og
```
