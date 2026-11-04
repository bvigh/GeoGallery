import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { createPinch } from "./gestures";
import { cameraFovFor, clampFov, rotateSpeedFor, wheelZoomFactor, zoomFov } from "./sphereMath";

export interface SphereViewState {
  yaw: number;
  pitch: number;
  /** Degrees across the shorter side of the viewport (see sphereMath.ts). */
  fov: number;
}

export interface SphereView {
  camera: THREE.PerspectiveCamera;
  canvas: HTMLCanvasElement;
  /** The view on screen, in the stored convention. */
  getView(): SphereViewState;
  /** Short-side FOV on screen. */
  getFov(): number;
  setFov(fov: number): void;
  /** Turns the camera; the controls keep orbiting from there. */
  setOrientation(q: THREE.Quaternion): void;
  render(): void;
  dispose(): void;
}

export interface SphereViewHooks {
  /** A pointer went down or the view zoomed. */
  onInteract?: () => void;
  /** The browser took the WebGL context away (GPU reset, too many contexts); the canvas is blank. */
  onContextLost?: () => void;
  /** The context came back; three.js re-uploads everything and drawing resumes. */
  onContextRestored?: () => void;
}

const FORWARD = new THREE.Vector3(0, 0, -1);

// The renderer, camera and controls of a 360° viewer: a textured sphere seen from inside, turned
// by dragging (OrbitControls) and zoomed by changing the FOV with the wheel, a trackpad pinch or
// a two-finger pinch. It follows the size of `mount`, and disposes the texture with the rest.
//
// Returns null, with the texture disposed and nothing added to `mount`, when the device cannot
// create a WebGL context (old phone, blocked GPU, context limit): the caller shows a notice
// instead of letting the error unmount the app.
export function createSphereView(
  mount: HTMLElement,
  texture: THREE.Texture,
  initial: SphereViewState,
  hooks: SphereViewHooks = {}
): SphereView | null {
  const { onInteract, onContextLost, onContextRestored } = hooks;
  const size = () => ({ w: mount.clientWidth, h: mount.clientHeight });
  const { w, h } = size();

  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({ antialias: true });
  } catch {
    // three.js has already logged why.
    texture.dispose();
    return null;
  }
  // Phones have 2–3 device pixels per CSS pixel; at 1 the picture is blurry. 2 is enough.
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  renderer.setSize(w, h);
  const canvas = renderer.domElement;
  // Out of the flow, so the canvas never holds its container open when the viewport shrinks.
  canvas.style.position = "absolute";
  canvas.style.top = "0";
  canvas.style.left = "0";
  mount.appendChild(canvas);

  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(75, w > 0 && h > 0 ? w / h : 1, 0.1, 1000);
  camera.position.set(0, 0, 0.001);

  const controls = new OrbitControls(camera, canvas);
  controls.enableZoom = false; // position zoom is wrong inside a sphere; FOV zoom used instead
  controls.enablePan = false;
  // Two fingers turn the view by their midpoint while the pinch below zooms. The default
  // (DOLLY_PAN) ignores them with zoom and pan off, so the view jumped to the midpoint later.
  controls.touches.TWO = THREE.TOUCH.DOLLY_ROTATE;

  const geometry = new THREE.SphereGeometry(500, 60, 40);
  geometry.scale(-1, 1, 1);
  const material = new THREE.MeshBasicMaterial({ map: texture });
  scene.add(new THREE.Mesh(geometry, material));

  // The wanted short-side FOV. A tall screen may show less (maxFovFor), and turning it back to
  // landscape restores the wider view.
  let fov = clampFov(initial.fov);
  const shownFov = () => clampFov(fov, camera.aspect);
  const applyFov = () => {
    camera.fov = cameraFovFor(fov, camera.aspect);
    camera.updateProjectionMatrix();
    controls.rotateSpeed = rotateSpeedFor(camera.fov);
  };
  applyFov();

  const setOrientation = (q: THREE.Quaternion) => {
    camera.quaternion.copy(q);
    controls.target.copy(camera.position).add(FORWARD.clone().applyQuaternion(camera.quaternion));
  };
  if (initial.yaw !== 0 || initial.pitch !== 0) {
    setOrientation(new THREE.Quaternion().setFromEuler(new THREE.Euler(initial.pitch, initial.yaw, 0, "YXZ")));
    controls.update();
  }

  const zoomBy = (factor: number) => {
    fov = zoomFov(shownFov(), factor, camera.aspect);
    applyFov();
    onInteract?.();
  };

  const onWheel = (e: WheelEvent) => {
    e.preventDefault();
    zoomBy(wheelZoomFactor(e));
  };

  const pinch = createPinch();
  const onPointerDown = (e: PointerEvent) => {
    pinch.down(e.pointerId, e.clientX, e.clientY);
    onInteract?.();
  };
  const onPointerMove = (e: PointerEvent) => {
    const factor = pinch.move(e.pointerId, e.clientX, e.clientY);
    if (factor !== null && factor !== 1) zoomBy(factor);
  };
  const onPointerUp = (e: PointerEvent) => pinch.up(e.pointerId);

  // three.js keeps its own listeners for these and restores the context when it can.
  const onLost = () => onContextLost?.();
  const onRestored = () => onContextRestored?.();
  canvas.addEventListener("webglcontextlost", onLost);
  canvas.addEventListener("webglcontextrestored", onRestored);
  canvas.addEventListener("wheel", onWheel, { passive: false });
  canvas.addEventListener("pointerdown", onPointerDown);
  canvas.addEventListener("pointermove", onPointerMove);
  canvas.addEventListener("pointerup", onPointerUp);
  canvas.addEventListener("pointercancel", onPointerUp);

  // A contained viewer changes size with its container (a modal's layout, a phone turned
  // sideways) without any window resize.
  const onResize = () => {
    const { w: w2, h: h2 } = size();
    if (w2 === 0 || h2 === 0) return; // hidden; keep the last good aspect
    camera.aspect = w2 / h2;
    renderer.setSize(w2, h2);
    applyFov();
  };
  const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(onResize) : null;
  if (observer) observer.observe(mount);
  else window.addEventListener("resize", onResize);

  return {
    camera,
    canvas,
    getView() {
      const euler = new THREE.Euler().setFromQuaternion(camera.quaternion, "YXZ");
      return { yaw: euler.y, pitch: euler.x, fov: shownFov() };
    },
    getFov: shownFov,
    setFov(next) {
      fov = clampFov(next);
      applyFov();
    },
    setOrientation,
    render() {
      controls.update();
      renderer.render(scene, camera);
    },
    dispose() {
      observer?.disconnect();
      window.removeEventListener("resize", onResize);
      // Before forceContextLoss below, which would otherwise report a loss.
      canvas.removeEventListener("webglcontextlost", onLost);
      canvas.removeEventListener("webglcontextrestored", onRestored);
      canvas.removeEventListener("wheel", onWheel);
      canvas.removeEventListener("pointerdown", onPointerDown);
      canvas.removeEventListener("pointermove", onPointerMove);
      canvas.removeEventListener("pointerup", onPointerUp);
      canvas.removeEventListener("pointercancel", onPointerUp);
      controls.dispose();
      // The scene before the renderer: renderer.dispose() forgets which GL objects it made, and a
      // texture disposed after it is never deleted from the GPU (a full-size frame per viewer).
      geometry.dispose();
      material.dispose();
      texture.dispose();
      renderer.dispose();
      // Frees the context and its drawing buffer now instead of at garbage collection; browsers
      // allow only ~16 live contexts and phones run out of GPU memory long before.
      renderer.forceContextLoss();
      mount.removeChild(canvas);
    },
  };
}
