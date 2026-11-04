import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as THREE from "three";
import { createSphereView } from "./sphereControls";
import { fakeGL, pointer, renderedMesh, rendered, resetRendered, stubLayout, stubPointerCapture, wheel } from "./viewerTestUtils";

// jsdom has no WebGL. The rest of three.js runs for real.
vi.mock("three", async (importOriginal) => {
  const { withFakeRenderer } = await import("./viewerTestUtils");
  return withFakeRenderer(await importOriginal());
});

const INITIAL = { yaw: 0, pitch: 0, fov: 75 };
let mount: HTMLDivElement;
let unstubCapture: () => void;

beforeEach(() => {
  resetRendered();
  stubLayout({ w: 800, h: 450 });
  unstubCapture = stubPointerCapture();
  mount = document.createElement("div");
  document.body.append(mount);
});

afterEach(() => {
  mount.remove();
  vi.restoreAllMocks();
  unstubCapture();
});

function logDispose(name: string, target: { addEventListener(type: "dispose", fn: () => void): void }) {
  target.addEventListener("dispose", () => rendered.log.push(`${name}.dispose`));
}

describe("createSphereView", () => {
  it("frees the scene before the renderer, then releases the context and the canvas", () => {
    const texture = new THREE.Texture();
    const view = createSphereView(mount, texture, INITIAL)!;
    view.render();
    const mesh = renderedMesh();
    logDispose("geometry", mesh.geometry);
    logDispose("material", mesh.material);
    logDispose("texture", texture);
    expect(mount.querySelector("canvas")).toBe(view.canvas);

    view.dispose();
    // renderer.dispose() forgets the GL objects it made, so anything disposed later stays on the GPU.
    expect(rendered.log).toEqual([
      "geometry.dispose",
      "material.dispose",
      "texture.dispose",
      "renderer.dispose",
      "renderer.forceContextLoss",
    ]);
    expect(mount.querySelector("canvas")).toBeNull();
  });

  it("returns null and leaves the mount empty when WebGL is unavailable", () => {
    fakeGL.fail = true;
    const texture = new THREE.Texture();
    const disposed = vi.fn();
    texture.addEventListener("dispose", disposed);

    expect(createSphereView(mount, texture, INITIAL)).toBeNull();
    expect(disposed).toHaveBeenCalledOnce();
    expect(mount.childElementCount).toBe(0);
  });

  it("reports a lost and a restored context, but not the loss it causes on dispose", () => {
    const onContextLost = vi.fn();
    const onContextRestored = vi.fn();
    const view = createSphereView(mount, new THREE.Texture(), INITIAL, { onContextLost, onContextRestored })!;
    const canvas = view.canvas;

    canvas.dispatchEvent(new Event("webglcontextlost"));
    expect(onContextLost).toHaveBeenCalledOnce();
    canvas.dispatchEvent(new Event("webglcontextrestored"));
    expect(onContextRestored).toHaveBeenCalledOnce();

    view.dispose();
    canvas.dispatchEvent(new Event("webglcontextlost"));
    expect(onContextLost).toHaveBeenCalledOnce();
  });

  it("reports interaction on a pointer down and on a zoom", () => {
    const onInteract = vi.fn();
    const view = createSphereView(mount, new THREE.Texture(), INITIAL, { onInteract })!;
    pointer(view.canvas, "pointerdown", 1, 100, 100);
    pointer(view.canvas, "pointerup", 1, 100, 100);
    expect(onInteract).toHaveBeenCalledTimes(1);
    wheel(view.canvas, 100);
    expect(onInteract).toHaveBeenCalledTimes(2);
    view.dispose();
  });
});
