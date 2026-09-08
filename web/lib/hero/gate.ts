export const SEEN_KEY = "kiln-hero-seen";
export const PENDING_CLASS = "intro-pending";

// The same test as the inline gate script in app/layout.tsx (GATE_SCRIPT). Change both
// together. `deliveryType` is populated before the inline script runs; `transferSize` is
// not, which is why it is never consulted.
export function isHardReload(): boolean {
  const nav = performance.getEntriesByType("navigation")[0] as
    | (PerformanceNavigationTiming & { deliveryType?: string })
    | undefined;
  return !!nav && nav.type === "reload" && nav.deliveryType !== "cache";
}

export function hasSeenThisSession(): boolean {
  try {
    return sessionStorage.getItem(SEEN_KEY) === "1";
  } catch {
    return false;
  }
}

export function markSeen(): void {
  try {
    sessionStorage.setItem(SEEN_KEY, "1");
  } catch {
    // Storage can be unavailable in private modes; the intro simply plays again next time.
  }
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function releaseArrivals(): void {
  document.documentElement.classList.remove(PENDING_CLASS);
}

export function logSkip(reason: string): void {
  console.info("[hero] skipped: " + reason);
}
