import type { Metadata } from "next";
import type React from "react";
import { IBM_Plex_Mono, IBM_Plex_Sans } from "next/font/google";
import { GATE_SCRIPT } from "@/lib/hero/gate-script";
import { site } from "@/content/site";
import "./globals.css";

const plex = IBM_Plex_Sans({
  subsets: ["latin"],
  weight: ["300", "400", "600"],
  variable: "--font-plex",
  display: "swap",
});

const plexMono = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500"],
  variable: "--font-plex-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: site.title,
  description: site.description,
  openGraph: {
    title: site.title,
    description: site.description,
    type: "website",
  },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${plex.variable} ${plexMono.variable}`}>
      <body>
        <script id="hero-gate" dangerouslySetInnerHTML={{ __html: GATE_SCRIPT }} />
        {children}
      </body>
    </html>
  );
}
