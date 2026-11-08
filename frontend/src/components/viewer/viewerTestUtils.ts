// Shared by the 360° viewer tests. jsdom has no WebGL, no layout and no pointer capture.
import { act } from "@testing-library/react";
import { vi } from "vitest";
import type * as THREE from "three";

// jsdom has no media playback. Like a real element, `paused` follows these two. Undo with
// vi.restoreAllMocks() or mockRestore().
export function stubMediaPlayback() {
  const play = vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (this: HTMLMediaElement) {
    Object.defineProperty(this, "paused", { value: false, configurable: true });
    return Promise.resolve();
  });
  const pause = vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(function (this: HTMLMediaElement) {
    Object.defineProperty(this, "paused", { value: true, configurable: true });
  });
  return { play, pause };
}

// What the fake renderer last drew with, and the renderer's lifecycle calls in order.
export const rendered: {
  count: number;
  camera: THREE.PerspectiveCamera | null;
  scene: THREE.Scene | null;
  log: string[];
} = { count: 0, camera: null, scene: null, log: [] };

// Set to make the next renderers throw like three.js does without a WebGL context.
export const fakeGL = { fail: false };

export function resetRendered() {
  rendered.count = 0;
  rendered.camera = null;
  rendered.scene = null;
  rendered.log = [];
  fakeGL.fail = false;
}

// For vi.mock("three"): the real three.js with a WebGLRenderer that only records.
export function withFakeRenderer(actual: typeof THREE) {
  class FakeRenderer {
    domElement = document.createElement("canvas");
    constructor() {
      if (fakeGL.fail) throw new Error("Error creating WebGL context.");
    }
    setPixelRatio() {}
    setSize() {}
    render(scene: THREE.Scene, camera: THREE.PerspectiveCamera) {
      rendered.count++;
      rendered.camera = camera;
      rendered.scene = scene;
    }
    dispose() {
      rendered.log.push("renderer.dispose");
    }
    forceContextLoss() {
      rendered.log.push("renderer.forceContextLoss");
    }
  }
  return { ...actual, WebGLRenderer: FakeRenderer };
}

// The sphere's mesh in the last rendered scene.
export function renderedMesh(): THREE.Mesh<THREE.BufferGeometry, THREE.MeshBasicMaterial> {
  const mesh = rendered.scene?.children[0];
  if (!mesh) throw new Error("nothing rendered");
  return mesh as THREE.Mesh<THREE.BufferGeometry, THREE.MeshBasicMaterial>;
}

// Every element reports this size; change it and fire a resize to "turn the phone".
export function stubLayout(size: { w: number; h: number }) {
  vi.spyOn(Element.prototype, "clientWidth", "get").mockImplementation(() => size.w);
  vi.spyOn(Element.prototype, "clientHeight", "get").mockImplementation(() => size.h);
}

// OrbitControls captures the first pointer; jsdom lacks the methods.
export function stubPointerCapture() {
  const proto = Element.prototype as unknown as Record<string, unknown>;
  const names = ["setPointerCapture", "releasePointerCapture"];
  const saved = names.map((name) => Object.getOwnPropertyDescriptor(proto, name));
  for (const name of names) proto[name] = () => {};
  return () => {
    names.forEach((name, i) => {
      const d = saved[i];
      if (d) Object.defineProperty(proto, name, d);
      else delete proto[name];
    });
  };
}

export function pointer(
  el: Element,
  type: "pointerdown" | "pointermove" | "pointerup" | "pointercancel",
  id: number,
  x: number,
  y: number,
  pointerType: "touch" | "mouse" = "touch"
) {
  act(() => {
    el.dispatchEvent(
      new PointerEvent(type, { pointerId: id, pointerType, clientX: x, clientY: y, button: 0, bubbles: true, cancelable: true })
    );
  });
}

// Two fingers spread symmetrically around (cx, cy) from `from` to `to` px apart.
export function pinch(el: Element, cx: number, cy: number, from: number, to: number) {
  pointer(el, "pointerdown", 1, cx - from / 2, cy);
  pointer(el, "pointerdown", 2, cx + from / 2, cy);
  for (let step = 1; step <= 4; step++) {
    const d = from + ((to - from) * step) / 4;
    pointer(el, "pointermove", 1, cx - d / 2, cy);
    pointer(el, "pointermove", 2, cx + d / 2, cy);
  }
  pointer(el, "pointerup", 2, cx + to / 2, cy);
  pointer(el, "pointerup", 1, cx - to / 2, cy);
}

export function wheel(el: Element, deltaY: number, ctrlKey = false) {
  const e = new WheelEvent("wheel", { deltaY, ctrlKey, bubbles: true, cancelable: true });
  act(() => {
    el.dispatchEvent(e);
  });
  return e;
}

// Replaces requestAnimationFrame, so a test steps the render loop itself: run() runs the frames
// queued so far, as one browser frame.
export function manualFrames() {
  let queue = new Map<number, FrameRequestCallback>();
  let next = 1;
  vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
    queue.set(next, cb);
    return next++;
  });
  vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation((id) => {
    queue.delete(id);
  });
  return {
    run() {
      const due = [...queue.values()];
      queue = new Map();
      act(() => due.forEach((cb) => cb(performance.now())));
    },
  };
}
