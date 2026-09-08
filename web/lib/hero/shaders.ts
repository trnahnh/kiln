export const VERTEX_SHADER = `
precision highp float;
attribute vec3 aLocal;
attribute vec3 aNormal;
attribute vec4 aRestQ;
attribute vec3 aRestP;
attribute vec4 aScatQ;
attribute vec3 aScatP;
attribute vec4 aAux;

uniform mat4 uProj;
uniform mat4 uView;
uniform float uT;
uniform float uFlight;
uniform float uScatScale;
uniform float uStrutLen;
uniform float uStrutGrow;

varying vec3 vN;
varying vec3 vP;
varying vec3 vAux;

vec3 rot(vec4 q, vec3 v) {
  return v + 2.0 * cross(q.xyz, cross(q.xyz, v) + q.w * v);
}

void main() {
  float kind = aAux.x;
  float lockAt = aAux.y;
  vec3 p;
  vec3 n;
  if (kind < 0.5) {
    float e = smoothstep(lockAt - uFlight, lockAt, uT);
    e = e * e * (3.0 - 2.0 * e);
    vec4 q = normalize(mix(aScatQ, aRestQ, e));
    float s = mix(uScatScale, 1.0, e);
    float loose = 1.0 - e;
    vec3 drift = vec3(
      sin(uT * 0.7 + aScatP.x * 7.0),
      cos(uT * 0.6 + aScatP.y * 5.0),
      sin(uT * 0.8 + aScatP.z * 6.0)
    ) * 0.05 * loose;
    p = mix(aScatP + drift, aRestP, e) + rot(q, aLocal * s);
    n = rot(q, aNormal);
  } else {
    float g = smoothstep(lockAt, lockAt + uStrutGrow, uT);
    vec3 local = aLocal;
    local.x = min(local.x, g * uStrutLen);
    p = aRestP + rot(aRestQ, local);
    n = rot(aRestQ, aNormal);
  }
  vN = n;
  vP = p;
  vAux = vec3(lockAt, aAux.z, kind);
  gl_Position = uProj * uView * vec4(p, 1.0);
}
`;

// Tone selection mirrors toneFor() in shading.ts. Change both together.
export const FRAGMENT_SHADER = `
precision highp float;

uniform vec3 uLightDir;
uniform vec3 uEye;
uniform float uT;
uniform float uFlash;
uniform vec3 uInk;
uniform vec3 uShadow;
uniform vec3 uDeep;
uniform vec3 uAccent;
uniform vec3 uHighlight;
uniform float uGlow[6];

varying vec3 vN;
varying vec3 vP;
varying vec3 vAux;

void main() {
  vec3 n = normalize(vN);
  vec3 v = normalize(uEye - vP);
  float lam = dot(n, uLightDir);
  float spec = pow(max(dot(reflect(-uLightDir, n), v), 0.0), 48.0);
  float lockAt = vAux.x;
  float locked = step(lockAt, uT);

  vec3 c = lam < 0.12 ? uShadow : (lam < 0.55 ? uDeep : uAccent);
  if (locked < 0.5 && lam >= 0.55) c = uDeep;
  if (locked > 0.5 && spec > 0.75) c = uHighlight;

  float flash = locked * (1.0 - smoothstep(lockAt, lockAt + uFlash, uT));
  c = mix(c, uHighlight, flash * 0.55 * step(0.12, lam));

  int node = int(vAux.y + 0.5);
  float glow = 0.0;
  for (int i = 0; i < 6; i++) {
    if (i == node) glow = uGlow[i];
  }
  vec3 lifted = lam < 0.12 ? uDeep : (lam < 0.55 ? uAccent : uHighlight);
  c = mix(c, lifted, glow * 0.7);

  c = mix(uInk, c, smoothstep(0.0, 0.5, uT));
  gl_FragColor = vec4(c, 1.0);
}
`;
