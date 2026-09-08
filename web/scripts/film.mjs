// Films the hero in real time over the DevTools protocol: navigates, then captures
// screenshots at fixed millisecond offsets while the scene actually plays. Optional
// --skip-at dispatches a pointer press at that offset. Frames land in --out.
//   node scripts/film.mjs --url http://localhost:3100 --out ../.film/desktop --width 1920 --height 1080 --dpr 1.5
//   --skip-at 1500 presses at that offset; --scrolls 5200:900,5600:1800 scrolls to y at ms.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { launch, openPage, closePage, sleep } from "./cdp.mjs";

const arg = (name, dflt) => {
  const i = process.argv.indexOf("--" + name);
  return i >= 0 ? process.argv[i + 1] : dflt;
};
const flag = (name) => process.argv.includes("--" + name);

const url = arg("url", "http://localhost:3100");
const out = arg("out", ".film");
const width = Number(arg("width", 1920));
const height = Number(arg("height", 1080));
const dpr = Number(arg("dpr", 1));
const mobile = flag("mobile");
const skipAt = arg("skip-at") ? Number(arg("skip-at")) : null;
const scrolls = (arg("scrolls", "") || "")
  .split(",")
  .filter(Boolean)
  .map((pair) => pair.split(":").map(Number));
const frames = arg("frames", "0,300,700,1100,1500,1900,2300,2700,3100,3500,3900,4300,5000")
  .split(",")
  .map(Number);

mkdirSync(out, { recursive: true });
const browser = await launch({ width, height });
const page = await openPage(browser.port, { width, height, deviceScaleFactor: dpr, mobile, hasTouch: mobile });
const consoleLines = [];
page.on("Runtime.consoleAPICalled", (p) => {
  consoleLines.push(p.type + ": " + p.args.map((a) => a.value ?? a.description ?? "").join(" "));
});
page.on("Runtime.exceptionThrown", (p) => consoleLines.push("exception: " + (p.exceptionDetails.exception?.description ?? p.exceptionDetails.text)));

await page.send("Page.navigate", { url });
const t0 = performance.now();
const events = [...frames.map((ms) => ({ ms, kind: "frame" }))];
if (skipAt !== null) events.push({ ms: skipAt, kind: "skip" });
for (const [ms, y] of scrolls) events.push({ ms, kind: "scroll", y });
events.sort((a, b) => a.ms - b.ms);

for (const ev of events) {
  const wait = ev.ms - (performance.now() - t0);
  if (wait > 0) await sleep(wait);
  const at = Math.round(performance.now() - t0);
  if (ev.kind === "skip") {
    const x = Math.round(width / 2);
    const y = Math.round(height / 2);
    await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
    await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
    console.log(`skip dispatched at ${at} ms`);
  } else if (ev.kind === "scroll") {
    await page.send("Runtime.evaluate", { expression: `window.scrollTo({top: ${ev.y}, behavior: "instant"})` });
    console.log(`scrolled to ${ev.y} at ${at} ms`);
  } else {
    const shot = await page.sendRetry("Page.captureScreenshot", { format: "png" });
    const name = `f${String(ev.ms).padStart(5, "0")}.png`;
    writeFileSync(join(out, name), Buffer.from(shot.data, "base64"));
    console.log(`${name} captured at ${at} ms`);
  }
}

const state = await page.sendRetry("Runtime.evaluate", {
  expression: `JSON.stringify({
    pending: document.documentElement.classList.contains("intro-pending"),
    seen: (() => { try { return sessionStorage.getItem("kiln-hero-seen"); } catch (e) { return "n/a"; } })(),
    phase: document.querySelector(".hero-stage")?.dataset.phase ?? null,
    live: !!document.getElementById("hero-slot")?.dataset.live,
    labels: [...document.querySelectorAll(".hero-label")].map((l) => l.textContent + "@" + l.style.opacity + " " + l.style.transform.replace(/translate\((.*?)px, (.*?)px\).*/, "$1,$2")),
  })`,
  returnByValue: true,
});
console.log("state:", state.result.value);
console.log("console:");
for (const line of consoleLines) console.log("  " + line);
writeFileSync(join(out, "console.txt"), consoleLines.join("\n") + "\nstate: " + state.result.value + "\n");

await closePage(browser.port, page);
browser.close();
