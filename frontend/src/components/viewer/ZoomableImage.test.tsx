import { createRef } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DOUBLE_TAP_MS, ZoomableImage, type ZoomableImageHandle } from "./ZoomableImage";

// jsdom has no layout. The container is a 400 x 300 box at (10, 20) on the page; the image is
// 800 x 400, so it fits to the width and leaves 50 px letterboxes above and below
// (local y 50..250).
const LEFT = 10;
const TOP = 20;

function layout(img: HTMLImageElement, natural: { width: number; height: number } | null = { width: 800, height: 400 }) {
  const box = img.parentElement as HTMLDivElement;
  box.getBoundingClientRect = () =>
    ({ left: LEFT, top: TOP, x: LEFT, y: TOP, width: 400, height: 300, right: LEFT + 400, bottom: TOP + 300 }) as DOMRect;
  if (natural) {
    Object.defineProperty(img, "naturalWidth", { value: natural.width, configurable: true });
    Object.defineProperty(img, "naturalHeight", { value: natural.height, configurable: true });
  }
  return box;
}

function setup(src = "https://s3.example/a.jpg") {
  const onTap = vi.fn();
  const onSwipe = vi.fn();
  const onZoomChange = vi.fn();
  const parentClick = vi.fn();
  const ref = createRef<ZoomableImageHandle>();
  const props = { alt: "photo", onTap, onSwipe, onZoomChange, ref };
  const utils = render(
    <div className="relative" onClick={parentClick}>
      <ZoomableImage src={src} {...props} />
    </div>,
  );
  const img = screen.getByRole("img", { name: "photo" }) as HTMLImageElement;
  const box = layout(img);
  const rerender = (next: string) =>
    utils.rerender(
      <div className="relative" onClick={parentClick}>
        <ZoomableImage src={next} {...props} />
      </div>,
    );
  return { img, box, onTap, onSwipe, onZoomChange, parentClick, ref, rerender };
}

// Pointer helpers take viewport-local coordinates.
type PointerType = "mouse" | "touch" | "pen";
function down(el: Element, x: number, y: number, pointerType: PointerType = "touch", pointerId = 1, button = 0) {
  fireEvent.pointerDown(el, { pointerId, pointerType, button, clientX: x + LEFT, clientY: y + TOP });
}
function move(el: Element, x: number, y: number, pointerType: PointerType = "touch", pointerId = 1) {
  fireEvent.pointerMove(el, { pointerId, pointerType, clientX: x + LEFT, clientY: y + TOP });
}
function up(el: Element, x: number, y: number, pointerType: PointerType = "touch", pointerId = 1) {
  fireEvent.pointerUp(el, { pointerId, pointerType, button: 0, clientX: x + LEFT, clientY: y + TOP });
}
// A tap as a browser delivers it: down, up, then the click.
function tap(el: Element, x: number, y: number, pointerType: PointerType = "touch") {
  down(el, x, y, pointerType);
  up(el, x, y, pointerType);
  fireEvent.click(el, { clientX: x + LEFT, clientY: y + TOP });
}
function drag(el: Element, from: [number, number], to: [number, number], pointerType: PointerType = "touch") {
  down(el, from[0], from[1], pointerType);
  move(el, (from[0] + to[0]) / 2, (from[1] + to[1]) / 2, pointerType);
  move(el, to[0], to[1], pointerType);
  up(el, to[0], to[1], pointerType);
}

function transformOf(img: HTMLImageElement) {
  const m = /translate\((-?[\d.e-]+)px, (-?[\d.e-]+)px\) scale\(([\d.e-]+)\)/.exec(img.style.transform);
  if (!m) return { x: 0, y: 0, scale: 1 };
  return { x: Number(m[1]), y: Number(m[2]), scale: Number(m[3]) };
}

function expectTransform(img: HTMLImageElement, want: { x: number; y: number; scale: number }) {
  const t = transformOf(img);
  expect(t.scale).toBeCloseTo(want.scale);
  expect(t.x).toBeCloseTo(want.x);
  expect(t.y).toBeCloseTo(want.y);
}

function elapse(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ZoomableImage", () => {
  it("renders an undraggable, contained image in a gesture-only box", () => {
    render(<ZoomableImage src="https://s3.example/a.jpg" alt="photo" crossOrigin="anonymous" className="bg-black" />);
    const img = screen.getByRole("img", { name: "photo" });
    expect(img).toHaveAttribute("src", "https://s3.example/a.jpg");
    expect(img).toHaveAttribute("draggable", "false");
    expect(img).toHaveAttribute("crossorigin", "anonymous");
    expect(img).toHaveClass("object-contain", "select-none");
    expect(img.style.transformOrigin).toBe("0 0");
    expect(img.parentElement).toHaveClass("absolute", "inset-0", "touch-none", "bg-black");
  });

  it("zooms around the cursor on the wheel and keeps the page from scrolling", () => {
    const { img, box, onZoomChange } = setup();
    // A trackpad pinch: ctrlKey, capped at 2x per event.
    const notPrevented = fireEvent.wheel(box, { deltaY: -100, ctrlKey: true, clientX: 100 + LEFT, clientY: 100 + TOP });
    expect(notPrevented).toBe(false);
    // The point under the cursor, (100, 100), stays there.
    expectTransform(img, { scale: 2, x: -100, y: -100 });
    expect(onZoomChange).toHaveBeenCalledWith(true);
    expect(box).toHaveAttribute("data-zoomed", "true");
    expect(box).toHaveClass("cursor-grab");

    // Zooming out stops at fit.
    for (let i = 0; i < 5; i++) fireEvent.wheel(box, { deltaY: 100, ctrlKey: true, clientX: 300, clientY: 300 });
    expectTransform(img, { scale: 1, x: 0, y: 0 });
    expect(onZoomChange).toHaveBeenLastCalledWith(false);
  });

  it.each(["mouse", "touch"] as const)("toggles fit and 2.5x at the point of a double %s tap", (pointerType) => {
    const { img, box, onTap, onZoomChange } = setup();
    tap(box, 200, 150, pointerType);
    tap(box, 200, 150, pointerType);
    expectTransform(img, { scale: 2.5, x: -300, y: -225 });
    expect(img.style.transition).toContain("transform");
    expect(onZoomChange).toHaveBeenCalledWith(true);

    elapse(DOUBLE_TAP_MS * 2);
    tap(box, 120, 80, pointerType);
    tap(box, 120, 80, pointerType);
    expectTransform(img, { scale: 1, x: 0, y: 0 });
    expect(onZoomChange).toHaveBeenLastCalledWith(false);

    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
  });

  it("ignores the native dblclick, so a mouse double click toggles only once", () => {
    const { img, box } = setup();
    tap(box, 200, 150, "mouse");
    tap(box, 200, 150, "mouse");
    fireEvent.doubleClick(box, { clientX: 200 + LEFT, clientY: 150 + TOP });
    expect(transformOf(img).scale).toBeCloseTo(2.5);
  });

  it("reports a tap on the image once the double-tap window has passed", () => {
    const { box, onTap } = setup();
    tap(box, 200, 150);
    expect(onTap).not.toHaveBeenCalled();
    elapse(DOUBLE_TAP_MS - 1);
    expect(onTap).not.toHaveBeenCalled();
    elapse(1);
    expect(onTap).toHaveBeenCalledTimes(1);
    expect(onTap).toHaveBeenCalledWith(true);
  });

  it("reports a tap in the letterbox on its click, right away", () => {
    const { box, onTap, parentClick } = setup();
    down(box, 200, 20);
    up(box, 200, 20);
    // Not on pointerup: a parent that closes then would let the click land underneath.
    expect(onTap).not.toHaveBeenCalled();
    fireEvent.click(box, { clientX: 200 + LEFT, clientY: 20 + TOP });
    expect(onTap).toHaveBeenCalledWith(false);
    // Clicks are not stopped.
    expect(parentClick).toHaveBeenCalledTimes(1);
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).toHaveBeenCalledTimes(1);
  });

  it("treats two quick taps far apart as two single taps", () => {
    const { img, box, onTap } = setup();
    tap(box, 60, 150);
    elapse(100);
    tap(box, 340, 150);
    // The first one fires as soon as the second, too far away to pair with it, comes down.
    expect(onTap).toHaveBeenCalledTimes(1);
    elapse(DOUBLE_TAP_MS);
    expect(onTap).toHaveBeenCalledTimes(2);
    expect(onTap).toHaveBeenNthCalledWith(2, true);
    expect(transformOf(img).scale).toBe(1);
  });

  it("does not count a slow second tap as a double tap", () => {
    const { img, box, onTap } = setup();
    tap(box, 200, 150);
    elapse(DOUBLE_TAP_MS + 1);
    tap(box, 200, 150);
    elapse(DOUBLE_TAP_MS);
    expect(onTap).toHaveBeenCalledTimes(2);
    expect(transformOf(img).scale).toBe(1);
  });

  it.each([
    { name: "right to left", from: [300, 150], to: [200, 160], want: "next" },
    { name: "left to right", from: [100, 150], to: [220, 140], want: "prev" },
    { name: "with a pen", from: [300, 150], to: [100, 150], want: "next", pointerType: "pen" as const },
  ] as const)("swipes $name at fit", ({ from, to, want, ...rest }) => {
    const { img, box, onSwipe, onTap } = setup();
    drag(box, [from[0], from[1]], [to[0], to[1]], "pointerType" in rest ? rest.pointerType : "touch");
    expect(onSwipe).toHaveBeenCalledTimes(1);
    expect(onSwipe).toHaveBeenCalledWith(want);
    expect(transformOf(img).scale).toBe(1);
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
  });

  it.each([
    { name: "a vertical drag", from: [200, 50], to: [210, 250] },
    { name: "a short drag", from: [200, 150], to: [170, 150] },
  ])("does not swipe on $name", ({ from, to }) => {
    const { box, onSwipe, onTap } = setup();
    drag(box, [from[0], from[1]], [to[0], to[1]]);
    expect(onSwipe).not.toHaveBeenCalled();
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
  });

  it("does not swipe on a mouse drag at fit, so a laptop drag never changes the item", () => {
    const { img, box, onSwipe, onTap } = setup();
    drag(box, [300, 150], [100, 150], "mouse");
    drag(box, [100, 150], [300, 150], "mouse");
    expect(onSwipe).not.toHaveBeenCalled();
    expect(transformOf(img).scale).toBe(1);
    // A drag is not a click either.
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
  });

  it("counts a 5 px mouse nudge as a drag but the same finger wobble as a tap", () => {
    const { box, onTap } = setup();
    drag(box, [200, 150], [205, 150], "mouse");
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
    drag(box, [200, 150], [205, 150], "touch");
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).toHaveBeenCalledWith(true);
  });

  it("pans instead of swiping when zoomed, and stops at the edges", () => {
    const { img, box, onSwipe } = setup();
    tap(box, 200, 150);
    tap(box, 200, 150);
    elapse(DOUBLE_TAP_MS * 2);
    expectTransform(img, { scale: 2.5, x: -300, y: -225 });

    drag(box, [200, 150], [150, 130]);
    expectTransform(img, { scale: 2.5, x: -350, y: -245 });
    expect(onSwipe).not.toHaveBeenCalled();

    // Far to the right and down: the image's top-left corner stops at the viewport's.
    drag(box, [100, 100], [1100, 900]);
    // 2.5x of the image rect (0, 50, 400 x 200): its left edge at x = 0, its top at y = 0.
    expectTransform(img, { scale: 2.5, x: 0, y: -125 });
    expect(onSwipe).not.toHaveBeenCalled();
  });

  it("shows grab cursors for a zoomed mouse drag", () => {
    const { box } = setup();
    fireEvent.wheel(box, { deltaY: -100, ctrlKey: true, clientX: 200 + LEFT, clientY: 150 + TOP });
    expect(box).toHaveClass("cursor-grab");
    down(box, 200, 150, "mouse");
    expect(box).toHaveClass("cursor-grabbing");
    up(box, 200, 150, "mouse");
    expect(box).toHaveClass("cursor-grab");
  });

  it("shows a zoom-in cursor over the image only, at fit", () => {
    const { box } = setup();
    move(box, 200, 150, "mouse");
    expect(box).toHaveClass("cursor-zoom-in");
    move(box, 200, 20, "mouse");
    expect(box).not.toHaveClass("cursor-zoom-in");
  });

  it("pinches around the fingers' midpoint", () => {
    const { img, box, onSwipe, onTap, onZoomChange } = setup();
    down(box, 150, 150, "touch", 1);
    down(box, 250, 150, "touch", 2);
    move(box, 350, 150, "touch", 2);
    move(box, 50, 150, "touch", 1);
    // Three times the distance around (200, 150): that point stays put.
    expectTransform(img, { scale: 3, x: -400, y: -300 });

    // Both fingers move together: the content follows the midpoint.
    move(box, 70, 160, "touch", 1);
    move(box, 370, 160, "touch", 2);
    expectTransform(img, { scale: 3, x: -380, y: -290 });

    up(box, 70, 160, "touch", 1);
    up(box, 370, 160, "touch", 2);
    elapse(DOUBLE_TAP_MS * 2);
    expect(onZoomChange).toHaveBeenCalledTimes(1);
    expect(onSwipe).not.toHaveBeenCalled();
    expect(onTap).not.toHaveBeenCalled();
  });

  it("pans on with the finger left after a pinch", () => {
    const { img, box } = setup();
    down(box, 150, 150, "touch", 1);
    down(box, 250, 150, "touch", 2);
    move(box, 350, 150, "touch", 2);
    move(box, 50, 150, "touch", 1);
    up(box, 350, 150, "touch", 2);
    move(box, 30, 140, "touch", 1);
    expectTransform(img, { scale: 3, x: -420, y: -310 });
    up(box, 30, 140, "touch", 1);
  });

  it("does not shrink below fit on a pinch in", () => {
    const { img, box } = setup();
    down(box, 100, 150, "touch", 1);
    down(box, 300, 150, "touch", 2);
    move(box, 190, 150, "touch", 1);
    move(box, 210, 150, "touch", 2);
    expectTransform(img, { scale: 1, x: 0, y: 0 });
  });

  it("snaps to fit when a pinch ends a hair above it", () => {
    const { img, box, onZoomChange } = setup();
    down(box, 100, 150, "touch", 1);
    down(box, 300, 150, "touch", 2);
    move(box, 300.1, 150, "touch", 2);
    expect(img.style.transform).toBe("translate(0px, 0px) scale(1)");
    expect(onZoomChange).not.toHaveBeenCalled();
  });

  it("ignores a third finger", () => {
    const { img, box } = setup();
    down(box, 150, 150, "touch", 1);
    down(box, 250, 150, "touch", 2);
    down(box, 200, 250, "touch", 3);
    move(box, 0, 0, "touch", 3);
    expectTransform(img, { scale: 1, x: 0, y: 0 });
  });

  it("neither taps nor swipes on a cancelled pointer", () => {
    const { box, onTap, onSwipe } = setup();
    down(box, 300, 150);
    move(box, 100, 150);
    fireEvent.pointerCancel(box, { pointerId: 1, pointerType: "touch", clientX: 100 + LEFT, clientY: 150 + TOP });
    down(box, 200, 150, "touch", 2);
    fireEvent.pointerCancel(box, { pointerId: 2, pointerType: "touch", clientX: 200 + LEFT, clientY: 150 + TOP });
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
    expect(onSwipe).not.toHaveBeenCalled();
  });

  it("ignores the right mouse button", () => {
    const { img, box, onTap } = setup();
    down(box, 200, 150, "mouse", 1, 2);
    up(box, 200, 150, "mouse");
    down(box, 200, 150, "mouse", 1, 2);
    up(box, 200, 150, "mouse");
    elapse(DOUBLE_TAP_MS * 2);
    expect(transformOf(img).scale).toBe(1);
    expect(onTap).not.toHaveBeenCalled();
  });

  it("resets to fit when src changes", () => {
    const { img, box, onZoomChange, onTap, rerender } = setup();
    fireEvent.wheel(box, { deltaY: -100, ctrlKey: true, clientX: 200 + LEFT, clientY: 150 + TOP });
    tap(box, 200, 150);
    expect(box).toHaveAttribute("data-zoomed", "true");

    rerender("https://s3.example/b.jpg");
    expect(img).toHaveAttribute("src", "https://s3.example/b.jpg");
    expectTransform(img, { scale: 1, x: 0, y: 0 });
    expect(onZoomChange).toHaveBeenLastCalledWith(false);
    expect(box).toHaveAttribute("data-zoomed", "false");
    // The pending tap belonged to the old image.
    elapse(DOUBLE_TAP_MS * 2);
    expect(onTap).not.toHaveBeenCalled();
  });

  it("resets through its handle", () => {
    const { img, box, ref, onZoomChange } = setup();
    fireEvent.wheel(box, { deltaY: -100, ctrlKey: true, clientX: 200 + LEFT, clientY: 150 + TOP });
    act(() => ref.current!.reset());
    expectTransform(img, { scale: 1, x: 0, y: 0 });
    expect(onZoomChange).toHaveBeenLastCalledWith(false);
  });

  it("re-clamps once the image has loaded", () => {
    render(<ZoomableImage src="https://s3.example/a.jpg" alt="photo" />);
    const img = screen.getByRole("img", { name: "photo" }) as HTMLImageElement;
    const box = layout(img, null);
    // Not loaded: the whole box counts as the image, so 2x at the corner is in bounds.
    fireEvent.wheel(box, { deltaY: -100, ctrlKey: true, clientX: LEFT, clientY: TOP });
    expectTransform(img, { scale: 2, x: 0, y: 0 });

    // Loaded, it is 400 x 200 at fit: its top edge (y 50, 100 at 2x) moves up to the viewport's.
    Object.defineProperty(img, "naturalWidth", { value: 800, configurable: true });
    Object.defineProperty(img, "naturalHeight", { value: 400, configurable: true });
    fireEvent.load(img);
    expectTransform(img, { scale: 2, x: 0, y: -100 });
  });

  it("sets will-change only while moving", () => {
    const { img, box } = setup();
    down(box, 150, 150, "touch", 1);
    expect(img.style.willChange).toBe("transform");
    up(box, 150, 150, "touch", 1);
    expect(img.style.willChange).toBe("");

    fireEvent.wheel(box, { deltaY: -100, clientX: 200 + LEFT, clientY: 150 + TOP });
    expect(img.style.willChange).toBe("transform");
    elapse(1000);
    expect(img.style.willChange).toBe("");
  });
});
