import { useCallback, useEffect, useRef, useState } from "react";
import * as THREE from "three";
import { Pause, Play, Volume2, VolumeX } from "lucide-react";
import type { OrientationKeyframe } from "@/types";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { videoDecodeFailed } from "@/lib/video";
import VideoDecodeNotice from "./VideoDecodeNotice";
import ViewerHeader from "./ViewerHeader";
import { createTapDetector } from "./gestures";
import { createSphereView } from "./sphereControls";
import { DEFAULT_FOV } from "./sphereMath";
import WebGLNotice, { type SphereFailure } from "./WebGLNotice";

// Stable reference — avoids re-running useEffect when no keyframes are passed
const EMPTY_KEYFRAMES: OrientationKeyframe[] = [];

interface Props {
  url: string;
  onClose: () => void;
  /** Keyframe fov is in the same short-side convention as initialFov. */
  keyframes?: OrientationKeyframe[];
  initialYaw?: number | null;
  initialPitch?: number | null;
  /** Degrees across the shorter side of the viewport (see sphereMath.ts). */
  initialFov?: number | null;
  /** When true: fills parent container (no fixed overlay). Used in GamePage. */
  contained?: boolean;
  /** Returns the view on screen; fov in the same short-side convention as initialFov. */
  captureRef?: React.MutableRefObject<(() => { yaw: number; pitch: number; fov: number }) | null>;
  captureTimeRef?: React.MutableRefObject<(() => number) | null>;
  controlRef?: React.MutableRefObject<{ pause: () => void; seek: (t: number) => void; seekAndApply: (t: number) => void } | null>;
  /**
   * A tap or click on the picture plays or pauses (default true). The view editor turns it off:
   * there a click or a small nudge of the view would restart playback and change the time the
   * next keyframe stores. The play button and Space still work.
   */
  tapToPlay?: boolean;
}

function fmt(s: number) {
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}:${sec.toString().padStart(2, "0")}`;
}

function lerpAngle(a: number, b: number, t: number): number {
  const delta = ((b - a) % (2 * Math.PI) + 3 * Math.PI) % (2 * Math.PI) - Math.PI;
  return a + delta * t;
}

function interpolateKeyframes(
  kfs: OrientationKeyframe[],
  t: number
): { yaw: number; pitch: number; fov: number } | null {
  if (kfs.length === 0) return null;
  const first = kfs[0];
  if (t <= first.time) return { yaw: first.yaw, pitch: first.pitch, fov: first.fov };
  const last = kfs[kfs.length - 1];
  if (t >= last.time) return { yaw: last.yaw, pitch: last.pitch, fov: last.fov };

  for (let i = 0; i < kfs.length - 1; i++) {
    const a = kfs[i];
    const b = kfs[i + 1];
    if (t >= a.time && t < b.time) {
      const alpha = (t - a.time) / (b.time - a.time);
      return {
        yaw: lerpAngle(a.yaw, b.yaw, alpha),
        pitch: lerpAngle(a.pitch, b.pitch, alpha),
        fov: a.fov + (b.fov - a.fov) * alpha,
      };
    }
  }
  return null;
}

function orientation(yaw: number, pitch: number): THREE.Quaternion {
  return new THREE.Quaternion().setFromEuler(new THREE.Euler(pitch, yaw, 0, "YXZ"));
}

type Flash = { kind: "play" | "pause"; n: number };
const FLASH_MS = 800;

// A big play or pause icon that pops up in the middle and fades, so a tap on the video shows
// what it did. `n` remounts it, which restarts the animation on a quick second tap.
function usePlaybackFlash() {
  const [flash, setFlash] = useState<Flash | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const show = useCallback((kind: Flash["kind"]) => {
    clearTimeout(timer.current);
    setFlash((prev) => ({ kind, n: (prev?.n ?? 0) + 1 }));
    timer.current = setTimeout(() => setFlash(null), FLASH_MS);
  }, []);
  return [flash, show] as const;
}

const iconButton =
  "flex h-7 w-7 shrink-0 items-center justify-center rounded hover:bg-white/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:h-11 touch:w-11";

export default function Video360({
  url,
  onClose,
  keyframes = EMPTY_KEYFRAMES,
  initialYaw,
  initialPitch,
  initialFov,
  contained = false,
  captureRef,
  captureTimeRef,
  controlRef,
  tapToPlay = true,
}: Props) {
  const yaw = initialYaw ?? 0;
  const pitch = initialPitch ?? 0;
  const fov = initialFov ?? DEFAULT_FOV;
  const keyframesRef = useRef(keyframes);
  useEffect(() => { keyframesRef.current = keyframes; }, [keyframes]);
  const tapToPlayRef = useRef(tapToPlay);
  useEffect(() => { tapToPlayRef.current = tapToPlay; }, [tapToPlay]);
  const mountRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [playing, setPlaying] = useState(true);
  const [muted, setMuted] = useState(true);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  // The url whose picture the browser cannot decode; a new url starts unflagged.
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const [glFailure, setGlFailure] = useState<SphereFailure | null>(null);
  const [flash, showFlash] = usePlaybackFlash();

  useEffect(() => {
    const mount = mountRef.current!;

    // Off-DOM video element — required for Three.js VideoTexture
    const video = document.createElement("video");
    const texture = new THREE.VideoTexture(video);
    texture.colorSpace = THREE.SRGBColorSpace;

    // --- Keyframe automation ---
    let userOverride = false;
    let cooldownTimer: ReturnType<typeof setTimeout> | null = null;
    const COOLDOWN_MS = 5000;
    const BLEND_MS = 1000;
    let blendStartTime: number | null = null;
    const blendFromQuat = new THREE.Quaternion();
    let blendFromFov = fov;

    // Back on the keyframe path, blending over BLEND_MS from wherever the view is now.
    function resumeAutomation() {
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = null;
      blendFromQuat.copy(view.camera.quaternion);
      blendFromFov = view.getFov();
      blendStartTime = performance.now();
      userOverride = false;
    }

    // --- User override: disable automation on interaction, resume with blend after 5s inactivity ---
    function onUserInteract() {
      userOverride = true;
      blendStartTime = null;
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = setTimeout(resumeAutomation, COOLDOWN_MS);
    }

    const created = createSphereView(
      mount,
      texture,
      { yaw, pitch, fov },
      {
        onInteract: onUserInteract,
        onContextLost: () => {
          // No picture: stop the sound too rather than play on behind the notice.
          video.pause();
          setGlFailure("lost");
        },
        onContextRestored: () => setGlFailure(null),
      }
    );
    if (!created) {
      // Nothing loaded yet (no src), so nothing to undo; the texture is already disposed.
      setGlFailure("unavailable");
      return;
    }
    setGlFailure(null);
    const view = created;

    videoRef.current = video;
    video.crossOrigin = "anonymous";
    video.src = url;
    video.loop = true;
    video.muted = true; // start muted for autoplay
    video.playsInline = true;
    video.play().catch(() => {});

    const onLoadedMetadata = () => setDuration(video.duration);
    const onPlay = () => setPlaying(true);
    const onPause = () => setPlaying(false);
    const onDecodeCheck = () => {
      if (videoDecodeFailed(video)) setFailedUrl(url);
    };
    video.addEventListener("loadedmetadata", onLoadedMetadata);
    video.addEventListener("play", onPlay);
    video.addEventListener("pause", onPause);
    video.addEventListener("loadeddata", onDecodeCheck);
    video.addEventListener("error", onDecodeCheck);

    if (captureRef) {
      captureRef.current = view.getView;
    }
    if (captureTimeRef) {
      captureTimeRef.current = () => video.currentTime;
    }
    if (controlRef) {
      controlRef.current = {
        pause: () => video.pause(),
        seek: (t: number) => { video.currentTime = t; },
        seekAndApply: (t: number) => {
          video.currentTime = t;
          video.pause();
          const o = interpolateKeyframes(keyframesRef.current, t);
          if (o) {
            view.setOrientation(orientation(o.yaw, o.pitch));
            view.setFov(o.fov);
          }
        },
      };
    }

    function onTimeUpdate() {
      setCurrentTime(video.currentTime);
      onDecodeCheck();
    }
    video.addEventListener("timeupdate", onTimeUpdate);

    const togglePlayback = () => {
      if (video.paused) {
        video.play().catch(() => {});
        showFlash("play");
      } else {
        video.pause();
        showFlash("pause");
      }
    };

    const onKeyDown = (e: KeyboardEvent) => {
      // Not behind a dialog (the gallery's album menu), even with focus fallen back to <body>.
      if (keyBelongsToDialog(e)) return;
      const tag = (e.target as HTMLElement)?.tagName;
      const editable = (e.target as HTMLElement)?.isContentEditable;
      if (e.code === "Space" && tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT" && !editable) {
        e.preventDefault();
        togglePlayback();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    mount.focus();

    // A tap or click on the picture plays or pauses; drags and pinches only move the view.
    const canvas = view.canvas;
    const tap = createTapDetector();
    const onTapDown = (e: PointerEvent) =>
      tap.down(e.pointerId, e.clientX, e.clientY, e.timeStamp, e.button === 0, e.pointerType);
    const onTapMove = (e: PointerEvent) => tap.move(e.pointerId, e.clientX, e.clientY);
    const onTapUp = (e: PointerEvent) => {
      if (!tap.up(e.pointerId, e.clientX, e.clientY, e.timeStamp) || !tapToPlayRef.current) return;
      // A tap does not look around. Its pointerdown paused the keyframe path like a drag does,
      // which would leave the camera frozen for the cooldown once the video plays.
      resumeAutomation();
      togglePlayback();
    };
    const onTapCancel = (e: PointerEvent) => tap.cancel(e.pointerId);
    canvas.addEventListener("pointerdown", onTapDown);
    canvas.addEventListener("pointermove", onTapMove);
    canvas.addEventListener("pointerup", onTapUp);
    canvas.addEventListener("pointercancel", onTapCancel);

    let rafId: number;
    const animate = () => {
      rafId = requestAnimationFrame(animate);
      if (!userOverride && !video.paused) {
        const o = interpolateKeyframes(keyframesRef.current, video.currentTime);
        if (o) {
          let alpha = 1;
          if (blendStartTime !== null) {
            const elapsed = performance.now() - blendStartTime;
            alpha = Math.min(elapsed / BLEND_MS, 1);
            // ease-in-out cubic
            alpha = alpha < 0.5 ? 4 * alpha * alpha * alpha : 1 - Math.pow(-2 * alpha + 2, 3) / 2;
            if (elapsed >= BLEND_MS) blendStartTime = null;
          }

          view.setOrientation(
            new THREE.Quaternion().slerpQuaternions(blendFromQuat, orientation(o.yaw, o.pitch), alpha)
          );
          view.setFov(blendFromFov + (o.fov - blendFromFov) * alpha);
        }
      }
      // No texture.needsUpdate here: VideoTexture flags each newly presented frame itself
      // (requestVideoFrameCallback), so a paused or undecodable video uploads nothing. Flagging it
      // every tick re-sent the whole 5760×2880 frame to the GPU 60 times a second.
      view.render();
    };
    animate();

    return () => {
      if (captureRef) captureRef.current = null;
      if (captureTimeRef) captureTimeRef.current = null;
      if (controlRef) controlRef.current = null;
      cancelAnimationFrame(rafId);
      if (cooldownTimer) clearTimeout(cooldownTimer);
      video.removeEventListener("loadedmetadata", onLoadedMetadata);
      video.removeEventListener("play", onPlay);
      video.removeEventListener("pause", onPause);
      video.removeEventListener("loadeddata", onDecodeCheck);
      video.removeEventListener("error", onDecodeCheck);
      video.removeEventListener("timeupdate", onTimeUpdate);
      window.removeEventListener("keydown", onKeyDown);
      canvas.removeEventListener("pointerdown", onTapDown);
      canvas.removeEventListener("pointermove", onTapMove);
      canvas.removeEventListener("pointerup", onTapUp);
      canvas.removeEventListener("pointercancel", onTapCancel);
      video.pause();
      video.src = "";
      videoRef.current = null;
      view.dispose();
    };
  }, [url, yaw, pitch, fov, captureRef, captureTimeRef, controlRef, showFlash]);

  const togglePlay = () => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      video.play().catch(() => {});
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
    <div className="flex shrink-0 items-center gap-1 bg-black/70 px-2 py-1 text-sm text-white backdrop-blur-sm touch:py-0">
      <button type="button" onClick={togglePlay} className={iconButton} aria-label={playing ? "Pause" : "Play"}>
        {playing ? (
          <Pause className="h-4 w-4" fill="currentColor" aria-hidden="true" />
        ) : (
          <Play className="h-4 w-4" fill="currentColor" aria-hidden="true" />
        )}
      </button>
      <button type="button" onClick={toggleMute} className={iconButton} aria-label={muted ? "Unmute" : "Mute"}>
        {muted ? <VolumeX className="h-4 w-4" aria-hidden="true" /> : <Volume2 className="h-4 w-4" aria-hidden="true" />}
      </button>
      <span className="w-9 shrink-0 text-xs tabular-nums">{fmt(currentTime)}</span>
      {/* A tall box: tapping anywhere near the track seeks or grabs it. */}
      <input
        type="range"
        aria-label="Seek"
        aria-valuetext={`${fmt(currentTime)} of ${fmt(duration)}`}
        min={0}
        max={duration || 1}
        step={0.1}
        value={currentTime}
        onChange={(e) => seek(Number(e.target.value))}
        className="h-4 min-w-0 flex-1 cursor-pointer accent-white touch:h-11"
      />
      <span className="w-9 shrink-0 text-right text-xs tabular-nums">{fmt(duration)}</span>
    </div>
  );

  const flashIcon = flash && (
    <div
      key={flash.n}
      aria-hidden="true"
      data-testid="playback-flash"
      data-kind={flash.kind}
      className="pointer-events-none absolute inset-0 flex items-center justify-center"
    >
      <div className="rounded-full bg-black/50 p-4 text-white motion-safe:animate-[ping_0.8s_cubic-bezier(0,0,0.2,1)_forwards]">
        {flash.kind === "play" ? (
          <Play className="h-8 w-8" fill="currentColor" />
        ) : (
          <Pause className="h-8 w-8" fill="currentColor" />
        )}
      </div>
    </div>
  );

  const notice = glFailure ? <WebGLNotice failure={glFailure} /> : failedUrl === url && <VideoDecodeNotice />;
  // Without WebGL the video is never loaded, so there is nothing to control.
  const controlBar = glFailure !== "unavailable" && controls;

  const stage = (
    <div className="relative min-h-0 flex-1">
      <div ref={mountRef} className="absolute inset-0 overflow-hidden outline-none" tabIndex={-1} />
      {flashIcon}
      {notice}
    </div>
  );

  if (contained) {
    return (
      <div className="absolute inset-0 flex flex-col bg-black">
        {stage}
        {controlBar}
      </div>
    );
  }

  return (
    <div className="fixed inset-0 z-[1000] flex flex-col bg-black">
      <ViewerHeader title="360° video" onClose={onClose} />
      {stage}
      {controlBar}
    </div>
  );
}
