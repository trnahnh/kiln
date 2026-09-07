import type { Metadata } from "next";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const metadata: Metadata = {
  title: "kiln",
  robots: { index: false, follow: false },
};

// The social card, captured headless at 1200x630 into app/opengraph-image.png by
// `pnpm og`. The sheet is inlined so the page's fonts apply to the drawing's text.
export default function OgPage() {
  const svg = readFileSync(resolve(process.cwd(), "public/draft/card.svg"), "utf8");
  return <div className="h-[630px] w-[1200px] overflow-hidden [&>svg]:h-full [&>svg]:w-full" dangerouslySetInnerHTML={{ __html: svg }} />;
}
