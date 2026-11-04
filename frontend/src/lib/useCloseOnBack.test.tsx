import { StrictMode, useState } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BACK_DEPTH_KEY, useCloseOnBack } from "@/lib/useCloseOnBack";

type State = { page?: string; [BACK_DEPTH_KEY]?: number } | null;
const state = () => window.history.state as State;
const depth = () => state()?.[BACK_DEPTH_KEY] ?? 0;

// jsdom traverses history in two setTimeout(0) steps. Zero-delay timers run in the order they were
// set, so each round below lets one step land however slow the machine is: twelve rounds cover a
// pop that lands and leads to the next one, with room to spare.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

// The browser's back (or forward) button: resolves once the popstate has been handled.
async function navigate(delta: number) {
  await act(async () => {
    const popped = new Promise((resolve) =>
      window.addEventListener("popstate", resolve, { once: true }),
    );
    window.history.go(delta);
    await popped;
  });
  await settle();
}

// Lets a queued flush run without letting a traversal land. Not inside act: async act resolves on
// a macrotask, after which jsdom may already have traversed.
async function microtasks() {
  await Promise.resolve();
}

const closed = vi.fn();

function Layer({ level, open, onClose }: { level: number; open: number; onClose: (level: number) => void }) {
  useCloseOnBack(() => onClose(level));
  return (
    <div data-testid={`layer-${level}`}>
      {level < open && <Layer level={level + 1} open={open} onClose={onClose} />}
    </div>
  );
}

// Nested fullscreen views, like the gallery overlay with a 360 view on top.
function Stack({ initial = 0 }: { initial?: number }) {
  const [open, setOpen] = useState(initial);
  const onClose = (level: number) => {
    closed(level);
    setOpen(level - 1);
  };
  return (
    <>
      <button onClick={() => setOpen((n) => n + 1)}>open</button>
      <button onClick={() => setOpen((n) => n - 1)}>close top</button>
      <button onClick={() => setOpen(0)}>close all</button>
      {open > 0 && <Layer level={1} open={open} onClose={onClose} />}
    </>
  );
}

function View({ onClose, enabled }: { onClose: () => void; enabled?: boolean }) {
  useCloseOnBack(onClose, enabled);
  return <div data-testid="view" />;
}

const openLayers = () => screen.queryAllByTestId(/^layer-/).length;

async function openViews(n: number) {
  for (let i = 0; i < n; i++) {
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    await settle();
  }
}

beforeEach(() => {
  closed.mockReset();
  // An entry outside the app below the app's own one: popping too far lands there.
  window.history.pushState({ page: "outside" }, "");
  window.history.pushState({ page: "app" }, "");
});

afterEach(async () => {
  vi.restoreAllMocks();
  cleanup();
  await settle();
  // Every test leaves history where it started, so the next one starts clean.
  expect(depth()).toBe(0);
  expect(state()?.page).toBe("app");
});

describe("useCloseOnBack", () => {
  it("adds one entry that keeps the existing state, and back closes the view", async () => {
    render(<Stack />);
    await openViews(1);
    expect(state()).toEqual({ page: "app", [BACK_DEPTH_KEY]: 1 });

    await navigate(-1);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(openLayers()).toBe(0);
    expect(depth()).toBe(0);
    expect(state()?.page).toBe("app");
  });

  it("removes its entry without calling onClose when the view closes another way", async () => {
    render(<Stack />);
    await openViews(1);

    fireEvent.click(screen.getByRole("button", { name: "close top" }));
    await settle();
    expect(depth()).toBe(0);
    expect(closed).not.toHaveBeenCalled();
    expect(state()?.page).toBe("app");
  });

  it("pops its entry when the parent unmounts it", async () => {
    const { unmount } = render(<Stack />);
    await openViews(1);

    unmount();
    await settle();
    expect(depth()).toBe(0);
    expect(closed).not.toHaveBeenCalled();
  });

  it("closes only the innermost view on back", async () => {
    render(<Stack />);
    await openViews(2);
    expect(depth()).toBe(2);

    await navigate(-1);
    expect(closed.mock.calls).toEqual([[2]]);
    expect(openLayers()).toBe(1);
    expect(depth()).toBe(1);

    await navigate(-1);
    expect(closed.mock.calls).toEqual([[2], [1]]);
    expect(openLayers()).toBe(0);
  });

  it("keeps the outer view open when the inner one closes another way", async () => {
    render(<Stack />);
    await openViews(2);

    fireEvent.click(screen.getByRole("button", { name: "close top" }));
    await settle();
    expect(depth()).toBe(1);
    expect(openLayers()).toBe(1);
    expect(closed).not.toHaveBeenCalled();

    await navigate(-1);
    expect(closed.mock.calls).toEqual([[1]]);
  });

  it("closes every view a longer jump back passes, innermost first", async () => {
    render(<Stack />);
    await openViews(2);

    await navigate(-2);
    expect(closed.mock.calls).toEqual([[2], [1]]);
    expect(openLayers()).toBe(0);
  });

  it("pops both entries in one traversal when two views close in the same tick", async () => {
    render(<Stack />);
    await openViews(2);
    const go = vi.spyOn(window.history, "go");

    fireEvent.click(screen.getByRole("button", { name: "close all" }));
    await settle();
    expect(go.mock.calls).toEqual([[-2]]);
    expect(depth()).toBe(0);
    expect(state()?.page).toBe("app");
    expect(closed).not.toHaveBeenCalled();
  });

  it("waits for a pop in flight before popping again", async () => {
    render(<Stack />);
    await openViews(2);
    const go = vi.spyOn(window.history, "go");

    fireEvent.click(screen.getByRole("button", { name: "close top" }));
    await microtasks();
    expect(go.mock.calls).toEqual([[-1]]);
    expect(depth()).toBe(2); // not landed yet

    fireEvent.click(screen.getByRole("button", { name: "close all" }));
    await microtasks();
    expect(go).toHaveBeenCalledTimes(1);

    await settle();
    expect(go.mock.calls).toEqual([[-1], [-1]]);
    expect(depth()).toBe(0);
    expect(state()?.page).toBe("app");
    expect(closed).not.toHaveBeenCalled();
  });

  it("pushes a view opened during a pop in flight only after the pop lands", async () => {
    render(<Stack />);
    await openViews(1);

    fireEvent.click(screen.getByRole("button", { name: "close top" }));
    await microtasks();
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    await settle();
    expect(openLayers()).toBe(1);
    expect(depth()).toBe(1);
    expect(closed).not.toHaveBeenCalled();

    await navigate(-1);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(openLayers()).toBe(0);
  });

  it("hands the entry to a view that replaces another in the same tick", async () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = render(<View key="a" onClose={first} />);
    await settle();
    const push = vi.spyOn(window.history, "pushState");
    const go = vi.spyOn(window.history, "go");

    rerender(<View key="b" onClose={second} />);
    await settle();
    expect(push).not.toHaveBeenCalled();
    expect(go).not.toHaveBeenCalled();
    expect(depth()).toBe(1);

    await navigate(-1);
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });

  it("survives StrictMode's mount, unmount and mount with one entry", async () => {
    const push = vi.spyOn(window.history, "pushState");
    const go = vi.spyOn(window.history, "go");
    render(
      <StrictMode>
        <Stack initial={1} />
      </StrictMode>,
    );
    await settle();
    expect(push).toHaveBeenCalledTimes(1);
    expect(go).not.toHaveBeenCalled();
    expect(depth()).toBe(1);
    expect(openLayers()).toBe(1);
    expect(closed).not.toHaveBeenCalled();

    await navigate(-1);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(openLayers()).toBe(0);
    expect(depth()).toBe(0);
  });

  it("opens nested views in StrictMode one entry each", async () => {
    render(
      <StrictMode>
        <Stack />
      </StrictMode>,
    );
    await openViews(2);
    expect(depth()).toBe(2);

    await navigate(-1);
    expect(closed.mock.calls).toEqual([[2]]);
    expect(openLayers()).toBe(1);
  });

  it("owns an entry only while enabled", async () => {
    const onClose = vi.fn();
    const { rerender } = render(<View onClose={onClose} enabled={false} />);
    await settle();
    expect(depth()).toBe(0);

    rerender(<View onClose={onClose} enabled />);
    await settle();
    expect(depth()).toBe(1);

    rerender(<View onClose={onClose} enabled={false} />);
    await settle();
    expect(depth()).toBe(0);
    expect(onClose).not.toHaveBeenCalled();

    rerender(<View onClose={onClose} enabled />);
    await settle();
    await navigate(-1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("calls the latest onClose", async () => {
    const stale = vi.fn();
    const latest = vi.fn();
    const { rerender } = render(<View onClose={stale} />);
    await settle();
    rerender(<View onClose={latest} />);

    await navigate(-1);
    expect(latest).toHaveBeenCalledTimes(1);
    expect(stale).not.toHaveBeenCalled();
  });

  it("goes back again when forward lands on a closed view's entry", async () => {
    render(<Stack />);
    await openViews(1);
    await navigate(-1);
    expect(closed).toHaveBeenCalledTimes(1);

    await navigate(1);
    expect(depth()).toBe(0);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(openLayers()).toBe(0);
  });

  it("never pops entries it did not push, such as those left before a reload", async () => {
    window.history.replaceState({ page: "app", [BACK_DEPTH_KEY]: 2 }, "");
    const onClose = vi.fn();
    const { unmount } = render(<View onClose={onClose} />);
    await settle();
    expect(depth()).toBe(3);

    await navigate(-1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(depth()).toBe(2);

    unmount();
    await settle();
    expect(depth()).toBe(2);
    // Back to a clean app entry for afterEach.
    window.history.replaceState({ page: "app" }, "");
  });
});
