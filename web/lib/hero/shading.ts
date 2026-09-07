// Mirrors the tone selection in shaders.ts (fragment shader). Change both together.
export const TONE = { shadow: 0, deep: 1, accent: 2, highlight: 3 } as const;
export type Tone = (typeof TONE)[keyof typeof TONE];

export const SHADE = {
  shadowBelow: 0.12,
  deepBelow: 0.55,
  specularAbove: 0.75,
  specularPower: 48,
} as const;

export function toneFor(lambert: number, specular: number, locked: boolean): Tone {
  let tone: Tone =
    lambert < SHADE.shadowBelow ? TONE.shadow : lambert < SHADE.deepBelow ? TONE.deep : TONE.accent;
  if (!locked && tone === TONE.accent) tone = TONE.deep;
  if (locked && specular > SHADE.specularAbove) tone = TONE.highlight;
  return tone;
}
