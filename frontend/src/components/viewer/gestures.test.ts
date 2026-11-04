import { describe, expect, it } from "vitest";
import { TAP_MAX_MS, TAP_SLOP_MOUSE, TAP_SLOP_TOUCH, createPinch, createTapDetector, tapSlop } from "./gestures";

describe("createPinch", () => {
  it("reports nothing for one pointer", () => {
    const p = createPinch();
    p.down(1, 0, 0);
    expect(p.move(1, 50, 0)).toBeNull();
  });

  it("reports the spread of two pointers move by move", () => {
    const p = createPinch();
    p.down(1, 0, 0);
    p.down(2, 100, 0);
    expect(p.move(2, 200, 0)).toBeCloseTo(2);
    expect(p.move(1, 100, 0)).toBeCloseTo(0.5);
  });

  it("ignores a pointer it never saw go down", () => {
    const p = createPinch();
    p.down(1, 0, 0);
    p.down(2, 100, 0);
    expect(p.move(9, 500, 0)).toBeNull();
  });

  it("does not jump when a finger lifts and another lands", () => {
    const p = createPinch();
    p.down(1, 0, 0);
    p.down(2, 100, 0);
    p.up(2);
    expect(p.move(1, 10, 0)).toBeNull();
    p.down(3, 310, 0);
    // The new pair starts at 300 px apart; only moves from there count.
    expect(p.move(3, 610, 0)).toBeCloseTo(2);
  });

  it("measures the first two pointers; a third changes nothing", () => {
    const p = createPinch();
    p.down(1, 0, 0);
    p.down(2, 100, 0);
    p.down(3, 50, 50);
    expect(p.move(3, 500, 500)).toBe(1);
    p.up(1);
    // Now pointers 2 and 3 form the pair, measured from where they are.
    expect(p.move(2, 100, 0)).toBe(1);
  });

  it("survives two pointers on one spot", () => {
    const p = createPinch();
    p.down(1, 10, 10);
    p.down(2, 10, 10);
    expect(p.move(2, 10, 10)).toBeNull();
    expect(p.move(2, 20, 10)).toBeNull();
    expect(p.move(2, 30, 10)).toBeCloseTo(2);
  });
});

describe("tapSlop", () => {
  it.each([
    { pointerType: "mouse", want: TAP_SLOP_MOUSE },
    { pointerType: "touch", want: TAP_SLOP_TOUCH },
    { pointerType: "pen", want: TAP_SLOP_TOUCH },
    { pointerType: "", want: TAP_SLOP_TOUCH },
  ])("$pointerType: $want px", ({ pointerType, want }) => {
    expect(tapSlop(pointerType)).toBe(want);
  });

  it("is tighter for a mouse than for a finger", () => {
    expect(TAP_SLOP_MOUSE).toBeLessThan(TAP_SLOP_TOUCH);
  });
});

describe("createTapDetector", () => {
  it.each([
    { name: "a quick press in place", pointerType: "touch", dx: 0, ms: 100, want: true },
    { name: "a little wobble", pointerType: "touch", dx: TAP_SLOP_TOUCH, ms: 100, want: true },
    { name: "a drag", pointerType: "touch", dx: TAP_SLOP_TOUCH + 1, ms: 100, want: false },
    { name: "a long press", pointerType: "touch", dx: 0, ms: TAP_MAX_MS + 1, want: false },
    { name: "a pen wobble", pointerType: "pen", dx: TAP_SLOP_TOUCH, ms: 100, want: true },
    { name: "a mouse click", pointerType: "mouse", dx: 0, ms: 100, want: true },
    { name: "a mouse jitter", pointerType: "mouse", dx: TAP_SLOP_MOUSE, ms: 100, want: true },
    // A short nudge that fine-tunes a view, well inside a finger's slop.
    { name: "a 5 px mouse drag", pointerType: "mouse", dx: 5, ms: 100, want: false },
  ])("$name: tap = $want", ({ pointerType, dx, ms, want }) => {
    const t = createTapDetector();
    t.down(1, 100, 100, 1000, true, pointerType);
    t.move(1, 100 + dx, 100);
    expect(t.up(1, 100 + dx, 100, 1000 + ms)).toBe(want);
  });

  it("measures a mouse release against the mouse slop even without a move", () => {
    const t = createTapDetector();
    t.down(1, 100, 100, 0, true, "mouse");
    expect(t.up(1, 106, 100, 50)).toBe(false);
  });

  it("is not a tap when the pointer wandered off and came back", () => {
    const t = createTapDetector();
    t.down(1, 100, 100, 0, true, "touch");
    t.move(1, 200, 100);
    t.move(1, 100, 100);
    expect(t.up(1, 100, 100, 50)).toBe(false);
  });

  it("ignores the secondary mouse button", () => {
    const t = createTapDetector();
    t.down(1, 0, 0, 0, false, "mouse");
    expect(t.up(1, 0, 0, 50)).toBe(false);
  });

  it("never counts a pinch as a tap", () => {
    const t = createTapDetector();
    t.down(1, 0, 0, 0, true, "touch");
    t.down(2, 100, 0, 10, true, "touch");
    expect(t.up(2, 100, 0, 60)).toBe(false);
    expect(t.up(1, 0, 0, 80)).toBe(false);
  });

  it("taps again once every finger is up", () => {
    const t = createTapDetector();
    t.down(1, 0, 0, 0, true, "touch");
    t.down(2, 100, 0, 10, true, "touch");
    t.up(1, 0, 0, 50);
    t.up(2, 100, 0, 60);
    t.down(3, 0, 0, 100, true, "touch");
    expect(t.up(3, 0, 0, 150)).toBe(true);
  });

  it("forgets a cancelled pointer", () => {
    const t = createTapDetector();
    t.down(1, 0, 0, 0, true, "touch");
    t.cancel(1);
    expect(t.up(1, 0, 0, 50)).toBe(false);
    t.down(2, 0, 0, 100, true, "touch");
    expect(t.up(2, 0, 0, 150)).toBe(true);
  });
});
