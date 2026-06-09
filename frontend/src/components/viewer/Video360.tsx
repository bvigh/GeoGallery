import { useEffect, useRef, useState } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import type { OrientationKeyframe } from "../../types";

// Stable reference — avoids re-running useEffect when no keyframes are passed
const EMPTY_KEYFRAMES: OrientationKeyframe[] = [];

interface Props {
  url: string;
  onClose: () => void;
  keyframes?: OrientationKeyframe[];
  initialYaw?: number;
  initialPitch?: number;
  initialFov?: number;
  /** When true: fills parent container (no fixed overlay). Used in GamePage. */
  contained?: boolean;
}

function fmt(s: number) {
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}:${sec.toString().padStart(2, "0")}`;
}

export default function Video360({
  url,
  onClose,
  keyframes = EMPTY_KEYFRAMES,
  initialYaw = 0,
  initialPitch = 0,
  initialFov = 75,
  contained = false,
}: Props) {
  const mountRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [playing, setPlaying] = useState(true);
  const [muted, setMuted] = useState(true);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);

  useEffect(() => {
    const mount = mountRef.current!;
    const w = mount.clientWidth;
    const h = mount.clientHeight;

    // Off-DOM video element — required for Three.js VideoTexture
    const video = document.createElement("video");
    videoRef.current = video;
    video.src = url;
    video.crossOrigin = "anonymous";
    video.loop = true;
    video.muted = true; // start muted for autoplay
    video.playsInline = true;
    video.play().catch(() => {});

    const onLoadedMetadata = () => setDuration(video.duration);
    const onPlay = () => setPlaying(true);
    const onPause = () => setPlaying(false);
    video.addEventListener("loadedmetadata", onLoadedMetadata);
    video.addEventListener("play", onPlay);
    video.addEventListener("pause", onPause);

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

    const texture = new THREE.VideoTexture(video);
    texture.colorSpace = THREE.SRGBColorSpace;
    const material = new THREE.MeshBasicMaterial({ map: texture });
    const mesh = new THREE.Mesh(geometry, material);
    scene.add(mesh);

    // --- Keyframe automation ---
    let userOverride = false;
    let cooldownTimer: ReturnType<typeof setTimeout> | null = null;
    const COOLDOWN_MS = 5000;

    function interpolateKeyframes(
      t: number
    ): { yaw: number; pitch: number; fov: number } | null {
      if (keyframes.length === 0) return null;
      const first = keyframes[0];
      if (t <= first.time) return { yaw: first.yaw, pitch: first.pitch, fov: first.fov };
      const last = keyframes[keyframes.length - 1];
      if (t >= last.time) return { yaw: last.yaw, pitch: last.pitch, fov: last.fov };

      for (let i = 0; i < keyframes.length - 1; i++) {
        const a = keyframes[i];
        const b = keyframes[i + 1];
        if (t >= a.time && t < b.time) {
          const alpha = (t - a.time) / (b.time - a.time);
          return {
            yaw: a.yaw + (b.yaw - a.yaw) * alpha,
            pitch: a.pitch + (b.pitch - a.pitch) * alpha,
            fov: a.fov + (b.fov - a.fov) * alpha,
          };
        }
      }
      return null;
    }

    function onTimeUpdate() {
      setCurrentTime(video.currentTime);
      if (userOverride) return;
      const orientation = interpolateKeyframes(video.currentTime);
      if (!orientation) return;
      const euler = new THREE.Euler(orientation.pitch, orientation.yaw, 0, "YXZ");
      camera.quaternion.setFromEuler(euler);
      controls.target
        .copy(camera.position)
        .add(new THREE.Vector3(0, 0, -1).applyQuaternion(camera.quaternion));
      camera.fov = orientation.fov;
      camera.updateProjectionMatrix();
    }
    video.addEventListener("timeupdate", onTimeUpdate);

    // --- User override: disable automation on interaction, resume after 5s inactivity ---
    function onUserInteract() {
      userOverride = true;
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = setTimeout(() => {
        userOverride = false;
      }, COOLDOWN_MS);
    }

    const canvas = renderer.domElement;
    canvas.addEventListener("mousedown", onUserInteract);
    canvas.addEventListener("pointerdown", onUserInteract);
    canvas.addEventListener("touchstart", onUserInteract, { passive: true });

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
      texture.needsUpdate = true;
      renderer.render(scene, camera);
    };
    animate();

    return () => {
      cancelAnimationFrame(rafId);
      if (cooldownTimer) clearTimeout(cooldownTimer);
      video.removeEventListener("loadedmetadata", onLoadedMetadata);
      video.removeEventListener("play", onPlay);
      video.removeEventListener("pause", onPause);
      video.removeEventListener("timeupdate", onTimeUpdate);
      canvas.removeEventListener("mousedown", onUserInteract);
      canvas.removeEventListener("pointerdown", onUserInteract);
      canvas.removeEventListener("touchstart", onUserInteract);
      canvas.removeEventListener("wheel", onWheel);
      window.removeEventListener("resize", onResize);
      video.pause();
      video.src = "";
      videoRef.current = null;
      controls.dispose();
      renderer.dispose();
      geometry.dispose();
      material.dispose();
      texture.dispose();
      mount.removeChild(renderer.domElement);
    };
  }, [url, keyframes, initialYaw, initialPitch, initialFov]);

  const togglePlay = () => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      video.play();
    } else {
      video.pause();
    }
  };

  const toggleMute = () => {
    const video = videoRef.current;
    if (!video) return;
    video.muted = !video.muted;
    setMuted(video.muted);
  };

  const seek = (t: number) => {
    const video = videoRef.current;
    if (!video) return;
    video.currentTime = t;
  };

  const controls = (
    <div className="flex items-center gap-2 px-3 py-1.5 bg-black/70 text-white text-sm shrink-0 backdrop-blur-sm">
      <button onClick={togglePlay} className="w-5 text-center text-base leading-none" aria-label={playing ? "Pause" : "Play"}>
        {playing ? "⏸" : "▶"}
      </button>
      <button onClick={toggleMute} className="w-5 text-center text-base leading-none" aria-label={muted ? "Unmute" : "Mute"}>
        {muted ? "🔇" : "🔊"}
      </button>
      <span className="text-xs tabular-nums w-9 shrink-0">{fmt(currentTime)}</span>
      <input
        type="range"
        min={0}
        max={duration || 1}
        step={0.1}
        value={currentTime}
        onChange={(e) => seek(Number(e.target.value))}
        className="flex-1 h-1 accent-white cursor-pointer"
      />
      <span className="text-xs tabular-nums w-9 shrink-0 text-right">{fmt(duration)}</span>
    </div>
  );

  if (contained) {
    return (
      <div className="absolute inset-0 bg-black flex flex-col">
        <div ref={mountRef} className="flex-1" />
        {controls}
      </div>
    );
  }

  return (
    <div className="fixed inset-0 z-[1000] bg-black flex flex-col">
      <div className="flex justify-between items-center px-4 py-2 text-white text-sm shrink-0 bg-black/60">
        <span>360° Video — drag to look around · scroll to zoom</span>
        <button
          onClick={onClose}
          className="text-white hover:text-gray-300 text-xl leading-none"
        >
          ✕
        </button>
      </div>
      <div ref={mountRef} className="flex-1" />
      {controls}
    </div>
  );
}
