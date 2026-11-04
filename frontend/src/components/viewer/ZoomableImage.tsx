import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { cn } from "@/lib/utils";
import {
  IDENTITY,
  clampPan,
  clampScale,
  clampZoom,
  distance,
  doubleTapTarget,
  fitRect,
  isInsideImage,
  isZoomed,
  maxScale,
  midpoint,
  pinch,
  swipeDirection,
  toTransform,
  wheelZoomFactor,
  zoomAt,
  type Point,
  type Rect,
  type Size,
  type SwipeDirection,
  type ZoomState,
} from "@/lib/zoom";
import { tapSlop } from "./gestures";

// Two taps closer than this in time and space are a double tap.
export const DOUBLE_TAP_MS = 300;
const DOUBLE_TAP_DISTANCE = 40;
const ANIMATION_MS = 250;

export interface ZoomableImageHandle {
  // Back to fit, animated.
  reset: () => void;
}

export interface ZoomableImageProps {
  src: string;
  alt: string;
  crossOrigin?: "anonymous" | "use-credentials";
  // Classes for the container, which is absolute inset-0 inside a positioned parent.
  className?: string;
  // A mostly horizontal one-finger (or pen) swipe at fit, not while zoomed; right to left is
  // "next". Never for a mouse: on a laptop, dragging a photo sideways is not a request to page.
  onSwipe?: (direction: SwipeDirection) => void;
  // A single tap or click without movement. Outside the displayed image (the letterbox) it fires
  // on the click that follows the tap; on the image it fires DOUBLE_TAP_MS later, once no second
  // tap came. A double tap never fires it.
  onTap?: (insideImage: boolean) => void;
  // Whenever the image goes from fit to zoomed or back, including the reset on a new src.
  onZoomChange?: (zoomed: boolean) => void;
}

interface Geometry {
  left: number;
  top: number;
  viewport: Size;
  content: Rect;
  max: number;
}

interface Gesture {
  // Active pointers in viewport coordinates; at most two (extra fingers are ignored).
  pointers: Map<number, Point>;
  mode: "idle" | "drag" | "pinch";
  geometry: Geometry | null;
  pointerType: string;
  start: Point;
  startState: ZoomState;
  startMid: Point;
  startDist: number;
  moved: boolean;
  // A second finger joined at some point: never a tap or a swipe.
  multi: boolean;
  // This gesture began within the double-tap window of the previous tap.
  secondTap: boolean;
}

interface PendingTap {
  point: Point;
  time: number;
  timer: ReturnType<typeof setTimeout> | null;
}

function newGesture(): Gesture {
  return {
    pointers: new Map(),
    mode: "idle",
    geometry: null,
    pointerType: "mouse",
    start: { x: 0, y: 0 },
    startState: IDENTITY,
    startMid: { x: 0, y: 0 },
    startDist: 1,
    moved: false,
    multi: false,
    secondTap: false,
  };
}

// An image that fills its positioned parent (object-contain) and zooms with a two-finger pinch,
// double tap/click, mouse wheel or trackpad pinch, and pans by dragging while zoomed. A finger
// swipe at fit reports onSwipe. It resets to fit when src changes.
//
// Events and the parent: nothing is stopped. Pointer events and clicks bubble as usual, including
// the click a browser sends after a mouse drag or a double click, so an ancestor's onClick also
// fires for those; use onTap instead (e.g. close on onTap(false)). Wheel events inside are
// preventDefault-ed (no page scroll or browser zoom) but still bubble.
export const ZoomableImage = forwardRef<ZoomableImageHandle, ZoomableImageProps>(function ZoomableImage(
  { src, alt, crossOrigin, className, onSwipe, onTap, onZoomChange },
  ref,
) {
  const containerRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  // The transform lives in a ref and is written to the DOM directly: a gesture updates it on
  // every pointer move, which should not re-render.
  const zoomRef = useRef<ZoomState>(IDENTITY);
  const zoomedRef = useRef(false);
  const gestureRef = useRef<Gesture>(newGesture());
  const pendingTapRef = useRef<PendingTap | null>(null);
  const outsideTapRef = useRef(false);
  const settleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const callbacksRef = useRef({ onSwipe, onTap, onZoomChange });
  callbacksRef.current = { onSwipe, onTap, onZoomChange };

  const [zoomed, setZoomed] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [overImage, setOverImage] = useState(false);

  function geometry(): Geometry | null {
    const el = containerRef.current;
    if (!el) return null;
    const r = el.getBoundingClientRect();
    const viewport = { width: r.width, height: r.height };
    const img = imgRef.current;
    const natural = img && img.naturalWidth > 0 ? { width: img.naturalWidth, height: img.naturalHeight } : null;
    return { left: r.left, top: r.top, viewport, content: fitRect(natural, viewport), max: maxScale(natural, viewport) };
  }

  function local(e: { clientX: number; clientY: number }, g: Geometry): Point {
    return { x: e.clientX - g.left, y: e.clientY - g.top };
  }

  // will-change only while moving: a layer that keeps it is not re-rasterized after a zoom,
  // so the zoomed image would stay blurry.
  function startMoving() {
    const img = imgRef.current;
    if (settleTimerRef.current) clearTimeout(settleTimerRef.current);
    settleTimerRef.current = null;
    if (img) img.style.willChange = "transform";
  }

  // later: after an animation, or once a wheel has been still for a moment.
  function stopMoving(later: boolean) {
    const img = imgRef.current;
    if (settleTimerRef.current) clearTimeout(settleTimerRef.current);
    settleTimerRef.current = null;
    if (!img) return;
    const settle = () => {
      settleTimerRef.current = null;
      img.style.willChange = "";
      img.style.transition = "";
    };
    if (later) settleTimerRef.current = setTimeout(settle, ANIMATION_MS + 50);
    else settle();
  }

  function commit(state: ZoomState, animate = false) {
    // A pinch or wheel ending a hair above fit snaps to it, so "not zoomed" is always exact.
    const next = isZoomed(state) ? state : IDENTITY;
    zoomRef.current = next;
    const img = imgRef.current;
    if (img) {
      if (animate) startMoving();
      img.style.transition = animate ? `transform ${ANIMATION_MS}ms ease-out` : "";
      img.style.transform = toTransform(next);
      if (animate) stopMoving(true);
    }
    const now = isZoomed(next);
    if (now !== zoomedRef.current) {
      zoomedRef.current = now;
      setZoomed(now);
      callbacksRef.current.onZoomChange?.(now);
    }
  }

  function clearPendingTap() {
    const pending = pendingTapRef.current;
    if (pending?.timer) clearTimeout(pending.timer);
    pendingTapRef.current = null;
  }

  // A new gesture: true when it may be the second tap of a double tap. A pending single tap on
  // the image that is too far away to pair with this one fires now instead of waiting.
  function takePendingTap(p: Point): boolean {
    const pending = pendingTapRef.current;
    if (!pending) return false;
    if (Date.now() - pending.time <= DOUBLE_TAP_MS && distance(p, pending.point) <= DOUBLE_TAP_DISTANCE) {
      if (pending.timer) clearTimeout(pending.timer);
      pending.timer = null;
      return true;
    }
    const fire = pending.timer !== null;
    clearPendingTap();
    if (fire) callbacksRef.current.onTap?.(true);
    return false;
  }

  function handleTap(p: Point, g: Gesture) {
    const geo = g.geometry;
    if (!geo) return;
    if (g.secondTap) {
      clearPendingTap();
      commit(doubleTapTarget(zoomRef.current, p, geo.viewport, geo.content, geo.max), true);
      return;
    }
    const inside = isInsideImage(p, zoomRef.current, geo.content);
    const pending: PendingTap = { point: p, time: Date.now(), timer: null };
    if (inside) {
      pending.timer = setTimeout(() => {
        pendingTapRef.current = null;
        callbacksRef.current.onTap?.(true);
      }, DOUBLE_TAP_MS);
    } else {
      // Fired from the click that follows: closing the parent on pointerup would let that click
      // land on whatever is underneath.
      outsideTapRef.current = true;
    }
    pendingTapRef.current = pending;
  }

  function reset(animate: boolean) {
    clearPendingTap();
    outsideTapRef.current = false;
    gestureRef.current = newGesture();
    setDragging(false);
    commit(IDENTITY, animate);
  }

  useImperativeHandle(ref, () => ({ reset: () => reset(true) }));

  // A new image starts at fit, before it paints.
  useLayoutEffect(() => {
    reset(false);
  }, [src]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    return () => {
      const pending = pendingTapRef.current;
      if (pending?.timer) clearTimeout(pending.timer);
      if (settleTimerRef.current) clearTimeout(settleTimerRef.current);
    };
  }, []);

  // React attaches wheel listeners as passive, and only a non-passive one may preventDefault.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    function onWheel(e: WheelEvent) {
      e.preventDefault();
      const geo = geometry();
      if (!geo || geo.viewport.width <= 0 || geo.viewport.height <= 0) return;
      const state = zoomRef.current;
      const factor = wheelZoomFactor(e.deltaY, e.deltaMode, e.ctrlKey, geo.viewport.height);
      const next = zoomAt(state, local(e, geo), clampScale(state.scale * factor, geo.max));
      startMoving();
      commit(clampPan(next, geo.viewport, geo.content));
      stopMoving(true);
    }
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // A resized viewport (rotation, window resize) changes the fit and the pan limits.
  useEffect(() => {
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => reclamp());
    observer.observe(el);
    return () => observer.disconnect();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  function reclamp() {
    if (!isZoomed(zoomRef.current)) return;
    const geo = geometry();
    if (!geo || geo.viewport.width <= 0 || geo.viewport.height <= 0) return;
    commit(clampZoom(zoomRef.current, geo.viewport, geo.content, geo.max));
  }

  function handlePointerDown(e: ReactPointerEvent<HTMLDivElement>) {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    const g = gestureRef.current;
    if (g.pointers.has(e.pointerId) || g.pointers.size >= 2) return;
    try {
      e.currentTarget.setPointerCapture?.(e.pointerId);
    } catch {
      // The pointer may already be gone; the gesture still works without capture.
    }
    if (g.pointers.size === 0) {
      const geo = geometry();
      if (!geo) return;
      const p = local(e, geo);
      outsideTapRef.current = false;
      g.mode = "drag";
      g.geometry = geo;
      g.pointerType = e.pointerType;
      g.start = p;
      g.startState = zoomRef.current;
      g.moved = false;
      g.multi = false;
      g.secondTap = takePendingTap(p);
      g.pointers.set(e.pointerId, p);
      startMoving();
      if (isZoomed(zoomRef.current)) setDragging(true);
      return;
    }
    const geo = g.geometry!;
    const p = local(e, geo);
    g.pointers.set(e.pointerId, p);
    const [a, b] = [...g.pointers.values()];
    g.mode = "pinch";
    g.multi = true;
    g.startState = zoomRef.current;
    g.startMid = midpoint(a, b);
    g.startDist = Math.max(1, distance(a, b));
  }

  function handlePointerMove(e: ReactPointerEvent<HTMLDivElement>) {
    const g = gestureRef.current;
    if (!g.pointers.has(e.pointerId) || !g.geometry) {
      if (e.pointerType === "mouse" && g.mode === "idle") {
        const geo = geometry();
        if (geo) setOverImage(isInsideImage(local(e, geo), zoomRef.current, geo.content));
      }
      return;
    }
    const geo = g.geometry;
    const p = local(e, geo);
    g.pointers.set(e.pointerId, p);
    if (g.mode === "pinch" && g.pointers.size === 2) {
      const [a, b] = [...g.pointers.values()];
      const next = pinch(
        g.startState,
        { mid: g.startMid, dist: g.startDist },
        { mid: midpoint(a, b), dist: distance(a, b) },
        geo.max,
      );
      commit(clampPan(next, geo.viewport, geo.content));
      return;
    }
    if (g.mode !== "drag") return;
    const dx = p.x - g.start.x;
    const dy = p.y - g.start.y;
    if (Math.hypot(dx, dy) > tapSlop(g.pointerType)) g.moved = true;
    if (isZoomed(g.startState)) {
      const next = { ...g.startState, x: g.startState.x + dx, y: g.startState.y + dy };
      commit(clampPan(next, geo.viewport, geo.content));
    }
  }

  function endPointer(e: ReactPointerEvent<HTMLDivElement>, cancelled: boolean) {
    const g = gestureRef.current;
    if (!g.pointers.has(e.pointerId) || !g.geometry) return;
    const geo = g.geometry;
    const p = local(e, geo);
    g.pointers.delete(e.pointerId);
    if (g.pointers.size === 1) {
      // One finger of a pinch lifted: the other one pans on from here.
      const [rest] = [...g.pointers.values()];
      g.mode = "drag";
      g.start = rest;
      g.startState = zoomRef.current;
      return;
    }
    g.mode = "idle";
    stopMoving(false);
    setDragging(false);
    const dx = p.x - g.start.x;
    const dy = p.y - g.start.y;
    if (Math.hypot(dx, dy) > tapSlop(g.pointerType)) g.moved = true;
    if (cancelled || g.multi || g.moved) {
      // The first tap of a double tap whose second half became a drag is dropped.
      if (g.secondTap) clearPendingTap();
      if (!cancelled && !g.multi && !isZoomed(g.startState) && g.pointerType !== "mouse") {
        const direction = swipeDirection(dx, dy);
        if (direction) callbacksRef.current.onSwipe?.(direction);
      }
      return;
    }
    handleTap(p, g);
  }

  function handleClick() {
    if (!outsideTapRef.current) return;
    outsideTapRef.current = false;
    callbacksRef.current.onTap?.(false);
  }

  return (
    <div
      ref={containerRef}
      data-zoomed={zoomed ? "true" : "false"}
      className={cn(
        "absolute inset-0 touch-none select-none overflow-hidden",
        zoomed ? (dragging ? "cursor-grabbing" : "cursor-grab") : overImage && "cursor-zoom-in",
        className,
      )}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={(e) => endPointer(e, false)}
      onPointerCancel={(e) => endPointer(e, true)}
      onPointerLeave={(e) => {
        if (e.pointerType === "mouse") setOverImage(false);
      }}
      onClick={handleClick}
    >
      <img
        ref={imgRef}
        src={src}
        alt={alt}
        crossOrigin={crossOrigin}
        draggable={false}
        onLoad={reclamp}
        onDragStart={(e) => e.preventDefault()}
        className="absolute inset-0 h-full w-full select-none object-contain [-webkit-touch-callout:none]"
        style={{ transformOrigin: "0 0" }}
      />
    </div>
  );
});

export default ZoomableImage;
