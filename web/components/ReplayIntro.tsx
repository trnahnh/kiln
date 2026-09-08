"use client";

import { useEffect, useState } from "react";
import { hasSeenThisSession } from "@/lib/hero/gate";

interface Props {
  variant?: "fixed" | "inline";
}

// A body-level fixed sibling of the overlay on desktop (hides on intro-shown, returns on
// intro-done); an inline link under the hero caption on phones, where a fixed pill would
// sit on top of the content.
export default function ReplayIntro({ variant = "fixed" }: Props) {
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
  const replay = () => window.dispatchEvent(new Event("replay-intro"));
  if (variant === "inline") {
    return (
      <button type="button" onClick={replay} className="mt-3 text-[13px] text-fg-faint underline underline-offset-4 md:hidden">
        Replay intro
      </button>
    );
  }
  return (
    <button
      type="button"
      onClick={replay}
      className="fixed bottom-5 right-5 z-[900] hidden rounded-full border border-hairline bg-ink px-3.5 py-1.5 text-xs text-fg-faint transition-colors hover:border-hairline-strong hover:text-fg-muted md:block"
    >
      Replay intro
    </button>
  );
}
