import type { Metadata } from "next";
import type React from "react";
import { Archivo, Big_Shoulders, IBM_Plex_Mono } from "next/font/google";
import { GATE_SCRIPT } from "@/lib/hero/gate-script";
import { site } from "@/content/site";
import "./globals.css";

const display = Big_Shoulders({
  subsets: ["latin"],
  weight: "variable",
  axes: ["opsz"],
  variable: "--font-big-shoulders",
  display: "swap",
});

const body = Archivo({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-archivo",
  display: "swap",
});

const mono = IBM_Plex_Mono({
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
    <html lang="en" className={`${display.variable} ${body.variable} ${mono.variable}`}>
      <body>
        <script id="hero-gate" dangerouslySetInnerHTML={{ __html: GATE_SCRIPT }} />
        {children}
      </body>
    </html>
  );
}
