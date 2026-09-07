// Verifies the intro's gating matrix headless, in real time: first open, normal reload,
// hard reload, route change and back, a new tab, reduced motion, and a no-WebGL run.
// Each case checks the sessionStorage flag, the stage phase, and the "[hero] skipped:"
// console line. Exits non-zero if any expectation fails.
//   node scripts/gating.mjs --url http://localhost:3100
import { launch, openPage, closePage, sleep } from "./cdp.mjs";

const arg = (name, dflt) => {
  const i = process.argv.indexOf("--" + name);
  return i >= 0 ? process.argv[i + 1] : dflt;
};
const url = arg("url", "http://localhost:3100");
const SETTLE_MS = Number(arg("settle", 1800));

const results = [];
function record(name, expectation, actual, ok) {
  results.push({ name, expectation, actual, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}\n      expected: ${expectation}\n      actual:   ${actual}`);
}

function watch(page) {
  const lines = [];
  page.on("Runtime.consoleAPICalled", (p) => {
    const text = p.args.map((a) => a.value ?? a.description ?? "").join(" ");
    if (text.startsWith("[hero]")) lines.push(text);
  });
  return lines;
}

async function state(page) {
  const r = await page.sendRetry("Runtime.evaluate", {
    returnByValue: true,
    expression: `JSON.stringify({
      seen: (() => { try { return sessionStorage.getItem("kiln-hero-seen"); } catch (e) { return "n/a"; } })(),
      phase: document.querySelector(".hero-stage")?.dataset.phase ?? null,
      pending: document.documentElement.classList.contains("intro-pending"),
      nav: (() => { const n = performance.getEntriesByType("navigation")[0]; return n ? n.type + "/" + (n.deliveryType ?? "undefined") : null; })(),
    })`,
  });
  return JSON.parse(r.result.value);
}

function describe(s, lines) {
  return `seen=${s.seen} phase=${s.phase} nav=${s.nav} skipped=${JSON.stringify(lines)}`;
}

async function run() {
  const browser = await launch({ width: 1280, height: 800 });
  const page = await openPage(browser.port, { width: 1280, height: 800 });
  let lines = watch(page);

  await page.send("Page.navigate", { url });
  await sleep(SETTLE_MS);
  let s = await state(page);
  record("first open plays", "seen=1, phase=intro, no skip line", describe(s, lines), s.seen === "1" && s.phase === "intro" && lines.length === 0);
  await sleep(4000);

  lines.length = 0;
  await page.send("Page.reload", { ignoreCache: false });
  await sleep(SETTLE_MS);
  s = await state(page);
  record(
    "normal reload does not replay",
    "phase=rest, skip line 'already seen this session'",
    describe(s, lines),
    s.phase === "rest" && lines.some((l) => l.includes("already seen this session")),
  );

  lines.length = 0;
  await page.send("Page.reload", { ignoreCache: true });
  await sleep(SETTLE_MS);
  s = await state(page);
  record("hard reload replays", "phase=intro, no skip line", describe(s, lines), s.phase === "intro" && lines.length === 0);
  await sleep(4000);

  lines.length = 0;
  await page.send("Page.navigate", { url: url.replace(/\/$/, "") + "/not-a-route" });
  await sleep(800);
  await page.send("Page.navigate", { url });
  await sleep(SETTLE_MS);
  s = await state(page);
  record(
    "route change and back does not replay",
    "phase=rest, skip line 'already seen this session'",
    describe(s, lines),
    s.phase === "rest" && lines.some((l) => l.includes("already seen this session")),
  );

  const tab = await openPage(browser.port, { width: 1280, height: 800, fresh: true });
  const tabLines = watch(tab);
  await tab.send("Page.navigate", { url });
  await sleep(SETTLE_MS);
  s = await state(tab);
  record("new tab plays", "seen=1, phase=intro, no skip line", describe(s, tabLines), s.seen === "1" && s.phase === "intro" && tabLines.length === 0);
  await closePage(browser.port, tab);

  const rm = await openPage(browser.port, { width: 1280, height: 800, fresh: true });
  const rmLines = watch(rm);
  await rm.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
  await rm.send("Page.navigate", { url });
  await sleep(SETTLE_MS);
  s = await state(rm);
  record(
    "reduced motion skips without writing the flag",
    "seen=null, phase=static, skip line 'prefers-reduced-motion is set'",
    describe(s, rmLines),
    s.seen === null && s.phase === "static" && rmLines.some((l) => l.includes("prefers-reduced-motion is set")),
  );
  await closePage(browser.port, rm);
  await closePage(browser.port, page);
  browser.close();

  const noGl = await launch({ width: 1280, height: 800, extraArgs: ["--disable-3d-apis"] });
  const gl = await openPage(noGl.port, { width: 1280, height: 800 });
  const glLines = watch(gl);
  await gl.send("Page.navigate", { url });
  await sleep(SETTLE_MS);
  s = await state(gl);
  record(
    "no WebGL skips without writing the flag",
    "seen=null, phase=static, skip line 'no WebGL context'",
    describe(s, glLines),
    s.seen === null && s.phase === "static" && glLines.some((l) => l.includes("no WebGL context")),
  );
  await closePage(noGl.port, gl);
  noGl.close();

  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} passed`);
  process.exit(failed.length ? 1 : 0);
}

run().catch((err) => {
  console.error(err);
  process.exit(2);
});
