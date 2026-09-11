"use client";

import { useEffect, useState } from "react";
import { hasSeenThisSession } from "@/lib/hero/gate";

// One control, in the hero's actions row on every viewport: hidden while the intro plays,
// back once it is done. A fixed pill used to stand in for it on desktop, and covered the
// footer; on phones it was a smaller link under the button that nobody found.
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
    <button type="button" onClick={() => window.dispatchEvent(new Event("replay-intro"))} className="link text-sm">
      Replay intro
    </button>
  );
}
