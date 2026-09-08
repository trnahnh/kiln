// A minimal Chrome DevTools Protocol client on Node's built-in WebSocket. Launches a
// headless Chromium in real time (no virtual time budget: WebGL frames must actually
// render), and hands back a session bound to one page.
import { spawn } from "node:child_process";
import { existsSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function findChromium() {
  if (process.env.CHROME_PATH && existsSync(process.env.CHROME_PATH)) return process.env.CHROME_PATH;
  const candidates = [];
  const pw = join(process.env.LOCALAPPDATA ?? join(homedir(), "AppData", "Local"), "ms-playwright");
  if (existsSync(pw)) {
    for (const dir of readdirSync(pw).filter((d) => d.startsWith("chromium-")).sort().reverse()) {
      candidates.push(join(pw, dir, "chrome-win64", "chrome.exe"), join(pw, dir, "chrome-win", "chrome.exe"));
      candidates.push(join(pw, dir, "chrome-linux", "chrome"), join(pw, dir, "chrome-mac", "Chromium.app/Contents/MacOS/Chromium"));
    }
  }
  candidates.push(
    "C:/Program Files/Google/Chrome/Application/chrome.exe",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
    "/usr/bin/google-chrome",
  );
  const found = candidates.find((c) => existsSync(c));
  if (!found) throw new Error("no Chromium found; set CHROME_PATH");
  return found;
}

export async function launch({ width = 1920, height = 1080, extraArgs = [] } = {}) {
  const exe = findChromium();
  const args = [
    "--headless=new",
    "--remote-debugging-port=0",
    "--no-first-run",
    "--no-default-browser-check",
    "--hide-scrollbars",
    "--use-angle=swiftshader",
    "--enable-unsafe-swiftshader",
    "--ignore-gpu-blocklist",
    `--window-size=${width},${height}`,
    "--user-data-dir=" + join(process.env.TEMP ?? "/tmp", "kiln-web-cdp-" + process.pid),
    ...extraArgs,
    "about:blank",
  ];
  const proc = spawn(exe, args, { stdio: ["ignore", "ignore", "pipe"] });
  const wsUrl = await new Promise((resolve, reject) => {
    let buf = "";
    proc.stderr.on("data", (d) => {
      buf += d.toString();
      const m = buf.match(/DevTools listening on (ws:\/\/\S+)/);
      if (m) resolve(m[1]);
    });
    proc.on("exit", (code) => reject(new Error("chromium exited early with " + code + "\n" + buf)));
    setTimeout(() => reject(new Error("chromium did not report a DevTools endpoint\n" + buf)), 15000);
  });
  const port = new URL(wsUrl).port;
  return { proc, port, close: () => proc.kill() };
}

export class Session {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.listeners = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id !== undefined) {
        const p = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (!p) return;
        if (msg.error) p.reject(new Error(p.method + ": " + msg.error.message));
        else p.resolve(msg.result);
      } else if (msg.method) {
        for (const fn of this.listeners.get(msg.method) ?? []) fn(msg.params);
      }
    });
  }

  send(method, params = {}) {
    const id = ++this.id;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject, method }));
  }

  // Commands issued while a navigation swaps renderer processes fail with
  // "Not attached to an active page"; the target comes back within a few dozen ms.
  async sendRetry(method, params = {}, attempts = 20) {
    for (let i = 0; ; i++) {
      try {
        return await this.send(method, params);
      } catch (err) {
        if (i >= attempts || !/Not attached/.test(err.message)) throw err;
        await sleep(50);
      }
    }
  }

  on(method, fn) {
    if (!this.listeners.has(method)) this.listeners.set(method, []);
    this.listeners.get(method).push(fn);
  }

  close() {
    this.ws.close();
  }
}

export async function openPage(port, { width, height, deviceScaleFactor = 1, mobile = false, hasTouch = false, fresh = false } = {}) {
  let target;
  if (fresh) {
    target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })).json();
    await sleep(300);
  } else {
    const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
    target = list.find((t) => t.type === "page");
    if (!target) throw new Error("no page target");
  }
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });
  const s = new Session(ws);
  s.targetId = target.id;
  // A freshly created target is not attachable for a few dozen milliseconds.
  for (let attempt = 0; ; attempt++) {
    try {
      await s.send("Page.enable");
      break;
    } catch (err) {
      if (attempt >= 20) throw err;
      await sleep(100);
    }
  }
  await s.send("Runtime.enable");
  if (width && height) {
    await s.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor, mobile });
    if (hasTouch) await s.send("Emulation.setTouchEmulationEnabled", { enabled: true });
  }
  return s;
}

export async function closePage(port, s) {
  s.close();
  await fetch(`http://127.0.0.1:${port}/json/close/${s.targetId}`).catch(() => {});
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
