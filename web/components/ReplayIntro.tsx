"use client";

import { useEffect, useState } from "react";
import { hasSeenThisSession } from "@/lib/hero/gate";

// A body-level fixed sibling of the overlay: hides on intro-shown, returns on intro-done.
export default function ReplayIntro() {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const show = () => setVisible(hasSeenThisSession());
    const hide = () => setVisible(false);
    show();
    window.addEventListener("intro-done", show);
    window.addEventListener("intro-shown", hide);
    return () => {
      window.removeEventListener("intro-done", show);
      window.removeEventListener("intro-shown", hide);
    };
  }, []);

  if (!visible) return null;
  return (
    <button
      type="button"
      onClick={() => window.dispatchEvent(new Event("replay-intro"))}
      className="fixed bottom-5 right-5 z-[900] rounded-full border border-hairline bg-ink px-3.5 py-1.5 text-xs text-fg-faint transition-colors hover:border-hairline-strong hover:text-fg-muted"
    >
      Replay intro
    </button>
  );
}
