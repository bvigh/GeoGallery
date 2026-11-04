import { useEffect, useRef, useState } from "react";
import * as THREE from "three";
import { createSphereView } from "./sphereControls";
import { DEFAULT_FOV } from "./sphereMath";
import ViewerHeader from "./ViewerHeader";
import WebGLNotice, { type SphereFailure } from "./WebGLNotice";

interface Props {
  url: string;
  onClose: () => void;
  initialYaw?: number | null;
  initialPitch?: number | null;
  /** Degrees across the shorter side of the viewport (see sphereMath.ts). */
  initialFov?: number | null;
  /** When true: fills parent container (no fixed overlay, no close bar). Used in GamePage. */
  contained?: boolean;
  /** Returns the view on screen; fov in the same short-side convention as initialFov. */
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
  const fov = initialFov ?? DEFAULT_FOV;
  const mountRef = useRef<HTMLDivElement>(null);
  const [glFailure, setGlFailure] = useState<SphereFailure | null>(null);

  useEffect(() => {
    const loader = new THREE.TextureLoader();
    loader.crossOrigin = "anonymous";
    const texture = loader.load(url);
    texture.colorSpace = THREE.SRGBColorSpace;

    const view = createSphereView(
      mountRef.current!,
      texture,
      { yaw, pitch, fov },
      { onContextLost: () => setGlFailure("lost"), onContextRestored: () => setGlFailure(null) }
    );
    if (!view) {
      setGlFailure("unavailable");
      return;
    }
    setGlFailure(null);
    if (captureRef) captureRef.current = view.getView;

    let rafId: number;
    const animate = () => {
      rafId = requestAnimationFrame(animate);
      view.render();
    };
    animate();

    return () => {
      if (captureRef) captureRef.current = null;
      cancelAnimationFrame(rafId);
      view.dispose();
    };
  }, [url, yaw, pitch, fov, captureRef]);

  const notice = glFailure && <WebGLNotice failure={glFailure} />;

  if (contained) {
    // The notice fills the same positioned parent as the mount.
    return (
      <>
        <div ref={mountRef} className="absolute inset-0 overflow-hidden bg-black" />
        {notice}
      </>
    );
  }

  return (
    <div className="fixed inset-0 z-[1000] flex flex-col bg-black">
      <ViewerHeader title="360° photo" onClose={onClose} />
      <div className="relative min-h-0 flex-1">
        <div ref={mountRef} className="absolute inset-0 overflow-hidden" />
        {notice}
      </div>
    </div>
  );
}
