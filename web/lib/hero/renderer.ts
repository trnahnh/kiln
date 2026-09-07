import type { Mat4, Vec3 } from "./math.ts";
import { FLOATS_PER_VERTEX, type Mesh } from "./geometry.ts";
import { VERTEX_SHADER, FRAGMENT_SHADER } from "./shaders.ts";
import { TL } from "./timeline.ts";

export type Rgb = [number, number, number];

export interface SceneColors {
  ink: Rgb;
  shadow: Rgb;
  deep: Rgb;
  accent: Rgb;
  highlight: Rgb;
}

export interface Frame {
  t: number;
  view: Mat4;
  proj: Mat4;
  eye: Vec3;
  light: Vec3;
  glow: Float32Array;
}

const ATTRIBUTES: [string, number][] = [
  ["aLocal", 3],
  ["aNormal", 3],
  ["aRestQ", 4],
  ["aRestP", 3],
  ["aScatQ", 4],
  ["aScatP", 3],
  ["aAux", 4],
];

export class HeroRenderer {
  private gl: WebGLRenderingContext;
  private program: WebGLProgram;
  private uniforms = new Map<string, WebGLUniformLocation | null>();
  private vertexCount: number;
  private strutLength: number;

  constructor(canvas: HTMLCanvasElement, mesh: Mesh, strutLength: number) {
    const gl = canvas.getContext("webgl", {
      alpha: true,
      antialias: true,
      depth: true,
      premultipliedAlpha: true,
      powerPreference: "high-performance",
    });
    if (!gl) throw new Error("no WebGL context");
    this.gl = gl;
    this.program = link(gl, VERTEX_SHADER, FRAGMENT_SHADER);
    this.vertexCount = mesh.vertexCount;
    this.strutLength = strutLength;

    const buffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
    gl.bufferData(gl.ARRAY_BUFFER, mesh.data, gl.STATIC_DRAW);
    gl.useProgram(this.program);

    const stride = FLOATS_PER_VERTEX * 4;
    let offset = 0;
    for (const [name, size] of ATTRIBUTES) {
      const loc = gl.getAttribLocation(this.program, name);
      if (loc >= 0) {
        gl.enableVertexAttribArray(loc);
        gl.vertexAttribPointer(loc, size, gl.FLOAT, false, stride, offset);
      }
      offset += size * 4;
    }

    for (const name of [
      "uProj", "uView", "uT", "uFlight", "uScatScale", "uStrutLen", "uStrutGrow",
      "uLightDir", "uEye", "uFlash", "uInk", "uShadow", "uDeep", "uAccent", "uHighlight", "uGlow",
    ]) {
      this.uniforms.set(name, gl.getUniformLocation(this.program, name));
    }

    gl.uniform1f(this.uniforms.get("uFlight")!, TL.flight);
    gl.uniform1f(this.uniforms.get("uScatScale")!, TL.scatterScale);
    gl.uniform1f(this.uniforms.get("uStrutLen")!, strutLength);
    gl.uniform1f(this.uniforms.get("uStrutGrow")!, TL.strutGrow);
    gl.uniform1f(this.uniforms.get("uFlash")!, TL.flash);

    gl.enable(gl.DEPTH_TEST);
    gl.enable(gl.CULL_FACE);
    gl.cullFace(gl.BACK);
    gl.frontFace(gl.CCW);
    gl.clearColor(0, 0, 0, 0);
  }

  resize(width: number, height: number): void {
    const canvas = this.gl.canvas as HTMLCanvasElement;
    if (canvas.width !== width || canvas.height !== height) {
      canvas.width = width;
      canvas.height = height;
    }
    this.gl.viewport(0, 0, width, height);
  }

  setColors(c: SceneColors): void {
    const gl = this.gl;
    gl.useProgram(this.program);
    gl.uniform3fv(this.uniforms.get("uInk")!, c.ink);
    gl.uniform3fv(this.uniforms.get("uShadow")!, c.shadow);
    gl.uniform3fv(this.uniforms.get("uDeep")!, c.deep);
    gl.uniform3fv(this.uniforms.get("uAccent")!, c.accent);
    gl.uniform3fv(this.uniforms.get("uHighlight")!, c.highlight);
  }

  render(f: Frame): void {
    const gl = this.gl;
    gl.useProgram(this.program);
    gl.uniformMatrix4fv(this.uniforms.get("uProj")!, false, f.proj);
    gl.uniformMatrix4fv(this.uniforms.get("uView")!, false, f.view);
    gl.uniform1f(this.uniforms.get("uT")!, f.t);
    gl.uniform3fv(this.uniforms.get("uLightDir")!, f.light);
    gl.uniform3fv(this.uniforms.get("uEye")!, f.eye);
    gl.uniform1fv(this.uniforms.get("uGlow")!, f.glow);
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.drawArrays(gl.TRIANGLES, 0, this.vertexCount);
  }

  dispose(): void {
    const ext = this.gl.getExtension("WEBGL_lose_context");
    ext?.loseContext();
  }
}

function compile(gl: WebGLRenderingContext, type: number, source: string): WebGLShader {
  const shader = gl.createShader(type);
  if (!shader) throw new Error("shader failed to link: createShader returned null");
  gl.shaderSource(shader, source);
  gl.compileShader(shader);
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    throw new Error("shader failed to link: " + (gl.getShaderInfoLog(shader) ?? "compile error"));
  }
  return shader;
}

function link(gl: WebGLRenderingContext, vs: string, fs: string): WebGLProgram {
  const program = gl.createProgram();
  if (!program) throw new Error("shader failed to link: createProgram returned null");
  gl.attachShader(program, compile(gl, gl.VERTEX_SHADER, vs));
  gl.attachShader(program, compile(gl, gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(program);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    throw new Error("shader failed to link: " + (gl.getProgramInfoLog(program) ?? "link error"));
  }
  return program;
}

export function cssColor(value: string): Rgb {
  const v = value.trim();
  const hex = v.match(/^#([0-9a-f]{6})$/i);
  if (hex) {
    const n = parseInt(hex[1], 16);
    return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
  }
  const rgb = v.match(/^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/i);
  if (rgb) return [Number(rgb[1]) / 255, Number(rgb[2]) / 255, Number(rgb[3]) / 255];
  throw new Error("unparseable colour: " + value);
}
