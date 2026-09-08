// Captures the /og route at 1200x630 into app/opengraph-image.png, the social card.
//   node scripts/og.mjs --url http://localhost:3100
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { launch, openPage, closePage, sleep } from "./cdp.mjs";

const i = process.argv.indexOf("--url");
const url = (i >= 0 ? process.argv[i + 1] : "http://localhost:3100").replace(/\/$/, "") + "/og";
const out = resolve(import.meta.dirname, "../app/opengraph-image.png");

const browser = await launch({ width: 1200, height: 630 });
const page = await openPage(browser.port, { width: 1200, height: 630, deviceScaleFactor: 1 });
await page.send("Page.navigate", { url });
await sleep(2500);
const shot = await page.sendRetry("Page.captureScreenshot", { format: "png" });
writeFileSync(out, Buffer.from(shot.data, "base64"));
console.log(`wrote ${out} (${Buffer.from(shot.data, "base64").length} B)`);
await closePage(browser.port, page);
browser.close();
