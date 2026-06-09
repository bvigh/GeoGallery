import { useEffect, useRef } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";

interface Props {
  url: string;
  onClose: () => void;
  initialYaw?: number;
  initialPitch?: number;
  initialFov?: number;
  /** When true: fills parent container (no fixed overlay, no close bar). Used in GamePage. */
  contained?: boolean;
}

export default function Panorama360({
  url,
  onClose,
  initialYaw = 0,
  initialPitch = 0,
  initialFov = 75,
  contained = false,
}: Props) {
  const mountRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const mount = mountRef.current!;
    const w = mount.clientWidth;
    const h = mount.clientHeight;

    const renderer = new THREE.WebGLRenderer({ antialias: true });
    renderer.setSize(w, h);
    mount.appendChild(renderer.domElement);

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(initialFov, w / h, 0.1, 1000);
    camera.position.set(0, 0, 0.001);

    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableZoom = false; // position zoom is wrong inside a sphere; FOV zoom used instead
    controls.enablePan = false;
    controls.rotateSpeed = -0.3;

    if (initialYaw !== 0 || initialPitch !== 0) {
      const euler = new THREE.Euler(initialPitch, initialYaw, 0, "YXZ");
      camera.quaternion.setFromEuler(euler);
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
  }, [url, initialYaw, initialPitch, initialFov]);

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
