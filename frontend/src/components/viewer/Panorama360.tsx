import { useEffect, useRef } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";

interface Props {
  url: string;
  onClose: () => void;
  initialYaw?: number | null;
  initialPitch?: number | null;
  initialFov?: number | null;
  /** When true: fills parent container (no fixed overlay, no close bar). Used in GamePage. */
  contained?: boolean;
  captureRef?: React.MutableRefObject<(() => { yaw: number; pitch: number; fov: number }) | null>;
}

export default function Panorama360({
  url,
  onClose,
  initialYaw,
  initialPitch,
  initialFov,
  contained = false,
  captureRef,
}: Props) {
  const yaw = initialYaw ?? 0;
  const pitch = initialPitch ?? 0;
  const fov = initialFov ?? 75;
  const mountRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const mount = mountRef.current!;
    const w = mount.clientWidth;
    const h = mount.clientHeight;

    const renderer = new THREE.WebGLRenderer({ antialias: true });
    renderer.setSize(w, h);
    mount.appendChild(renderer.domElement);

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(fov, w / h, 0.1, 1000);
    camera.position.set(0, 0, 0.001);

    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableZoom = false; // position zoom is wrong inside a sphere; FOV zoom used instead
    controls.enablePan = false;
    controls.rotateSpeed = -0.3;

    if (yaw !== 0 || pitch !== 0) {
      const euler = new THREE.Euler(pitch, yaw, 0, "YXZ");
      camera.quaternion.setFromEuler(euler);
      controls.target
        .copy(camera.position)
        .add(new THREE.Vector3(0, 0, -1).applyQuaternion(camera.quaternion));
      controls.update();
    }

    if (captureRef) {
      captureRef.current = () => {
        const euler = new THREE.Euler().setFromQuaternion(camera.quaternion, "YXZ");
        return { yaw: euler.y, pitch: euler.x, fov: camera.fov };
      };
    }

    const MIN_FOV = 30;
    const MAX_FOV = 100;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      camera.fov = Math.min(MAX_FOV, Math.max(MIN_FOV, camera.fov + e.deltaY * 0.05));
      camera.updateProjectionMatrix();
    };
    renderer.domElement.addEventListener("wheel", onWheel, { passive: false });

    const geometry = new THREE.SphereGeometry(500, 60, 40);
    geometry.scale(-1, 1, 1);

    const loader = new THREE.TextureLoader();
    loader.crossOrigin = "anonymous";
    const texture = loader.load(url);
    texture.colorSpace = THREE.SRGBColorSpace;
    const material = new THREE.MeshBasicMaterial({ map: texture });
    const mesh = new THREE.Mesh(geometry, material);
    scene.add(mesh);

    const onResize = () => {
      const w2 = mount.clientWidth;
      const h2 = mount.clientHeight;
      camera.aspect = w2 / h2;
      camera.updateProjectionMatrix();
      renderer.setSize(w2, h2);
    };
    window.addEventListener("resize", onResize);

    let rafId: number;
    const animate = () => {
      rafId = requestAnimationFrame(animate);
      controls.update();
      renderer.render(scene, camera);
    };
    animate();

    return () => {
      if (captureRef) captureRef.current = null;
      cancelAnimationFrame(rafId);
      window.removeEventListener("resize", onResize);
      renderer.domElement.removeEventListener("wheel", onWheel);
      controls.dispose();
      renderer.dispose();
      geometry.dispose();
      material.dispose();
      texture.dispose();
      mount.removeChild(renderer.domElement);
    };
  }, [url, yaw, pitch, fov, captureRef]);

  if (contained) {
    return <div ref={mountRef} className="absolute inset-0 bg-black" />;
  }

  return (
    <div className="fixed inset-0 z-[1000] bg-black flex flex-col">
      <div className="flex justify-between items-center px-4 py-2 text-white text-sm shrink-0 bg-black/60">
        <span>360° Photo — drag to look around · scroll to zoom</span>
        <button
          onClick={onClose}
          className="text-white hover:text-gray-300 text-xl leading-none"
        >
          ✕
        </button>
      </div>
      <div ref={mountRef} className="flex-1" />
    </div>
  );
}
