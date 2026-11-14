import { StrictMode, useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BACK_DEPTH_KEY,
  onHistoryChange,
  pushPage,
  startHistory,
  useCloseOnBack,
  useHistoryChange,
} from "@/lib/history";

type State = { page?: string; [BACK_DEPTH_KEY]?: number } | null;
const state = () => window.history.state as State;
const depth = () => state()?.[BACK_DEPTH_KEY] ?? 0;
const url = () => window.location.pathname + window.location.search;

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

function View({ onClose, enabled, url }: { onClose: () => void; enabled?: boolean; url?: string | null }) {
  useCloseOnBack(onClose, enabled, url);
  return <div data-testid="view" />;
}

// A gallery (with a URL) and, on top of it, a view without one (the evidence view, an editor).
function Gallery({
  url,
  inner,
  onClose,
  onCloseInner,
}: {
  url: string;
  inner: boolean;
  onClose: () => void;
  onCloseInner: () => void;
}) {
  useCloseOnBack(onClose, true, url);
  return <div data-testid="gallery">{inner && <View onClose={onCloseInner} />}</div>;
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
  window.history.pushState({ page: "outside" }, "", "/outside");
  window.history.pushState({ page: "app" }, "", "/trips");
});

afterEach(async () => {
  vi.restoreAllMocks();
  cleanup();
  await settle();
  // Every test leaves history where it started, so the next one starts clean.
  expect(depth()).toBe(0);
  expect(state()?.page).toBe("app");
  expect(url()).toBe("/trips");
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

describe("view URLs", () => {
  const T1 = "/trips/0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";

  it("shows a view's URL on its entry, and back returns to the page's", async () => {
    const onClose = vi.fn();
    render(<View onClose={onClose} url={`${T1}?item=a`} />);
    await settle();
    expect(url()).toBe(`${T1}?item=a`);
    expect(state()).toEqual({ page: "app", [BACK_DEPTH_KEY]: 1 });

    await navigate(-1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(url()).toBe("/trips");
  });

  it("keeps a view's URL on its own entry as it changes", async () => {
    const { rerender } = render(<View onClose={vi.fn()} url={`${T1}?item=a`} />);
    await settle();
    const length = window.history.length;

    rerender(<View onClose={vi.fn()} url={`${T1}?item=b`} />);
    await waitFor(() => expect(url()).toBe(`${T1}?item=b`));
    expect(window.history.length).toBe(length);
    expect(depth()).toBe(1);
  });

  it("writes a changing URL at most every 250 ms, and the last one lands", async () => {
    const { rerender } = render(<View onClose={vi.fn()} url={`${T1}?item=a`} />);
    await settle();
    const replace = vi.spyOn(window.history, "replaceState");

    for (const item of ["b", "c", "d", "e"]) {
      rerender(<View onClose={vi.fn()} url={`${T1}?item=${item}`} />);
      await microtasks();
    }
    expect(replace.mock.calls.length).toBeLessThanOrEqual(1);
    await waitFor(() => expect(url()).toBe(`${T1}?item=e`));
    expect(replace.mock.calls.length).toBeLessThanOrEqual(2);
  });

  it("keeps the view's URL on an inner view's entry, and on its own entry once the inner view closes", async () => {
    const onClose = vi.fn();
    const onCloseInner = vi.fn();
    const props = { onClose, onCloseInner };
    const { rerender } = render(<Gallery url={`${T1}?item=a`} inner={false} {...props} />);
    await settle();
    rerender(<Gallery url={`${T1}?item=a`} inner {...props} />);
    await settle();
    expect(depth()).toBe(2);
    expect(url()).toBe(`${T1}?item=a`);

    // The evidence view steps to another item of the gallery.
    rerender(<Gallery url={`${T1}?item=b`} inner {...props} />);
    await waitFor(() => expect(url()).toBe(`${T1}?item=b`));

    await navigate(-1);
    expect(onCloseInner).toHaveBeenCalledTimes(1);
    rerender(<Gallery url={`${T1}?item=b`} inner={false} {...props} />);
    expect(depth()).toBe(1);
    await waitFor(() => expect(url()).toBe(`${T1}?item=b`));

    await navigate(-1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(url()).toBe("/trips");
  });

  it("gives an inner view the latest URL even before it was written", async () => {
    const props = { onClose: vi.fn(), onCloseInner: vi.fn() };
    const { rerender } = render(<Gallery url={`${T1}?item=a`} inner={false} {...props} />);
    await settle();
    rerender(<Gallery url={`${T1}?item=b`} inner={false} {...props} />);
    await microtasks();
    rerender(<Gallery url={`${T1}?item=c`} inner={false} {...props} />);
    rerender(<Gallery url={`${T1}?item=c`} inner {...props} />);
    await settle();
    expect(depth()).toBe(2);
    expect(url()).toBe(`${T1}?item=c`);
  });

  it("returns to the page's URL when the view closes another way", async () => {
    const { unmount } = render(<View onClose={vi.fn()} url={`${T1}?item=a`} />);
    await settle();
    unmount();
    await settle();
    expect(depth()).toBe(0);
    expect(url()).toBe("/trips");
  });

  it("opens a view from a link on an entry above its page's, which back returns to", async () => {
    window.history.replaceState({ page: "app" }, "", `${T1}?item=a`);
    startHistory(`${T1}?item=a`, "/trips");
    expect(state()).toEqual({ page: "app", [BACK_DEPTH_KEY]: 1 });
    const push = vi.spyOn(window.history, "pushState");
    const onClose = vi.fn();
    render(<View onClose={onClose} url={`${T1}?item=a`} />);
    await settle();
    expect(push).not.toHaveBeenCalled();
    expect(depth()).toBe(1);

    await navigate(-1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(url()).toBe("/trips");
  });

  it("writes the canonical URL and adds no entry for a page", () => {
    window.history.replaceState({ page: "app" }, "", "/trips/");
    const length = window.history.length;
    startHistory("/trips", "/trips");
    expect(url()).toBe("/trips");
    expect(window.history.length).toBe(length);
  });

  it.each([
    ["back", -1],
    ["the close button", 0],
  ])(
    "after a reload, takes over the view's entry and those of views closed by it, and %s leaves them all",
    async (_how, delta) => {
      // A gallery with the evidence view over it, reloaded.
      window.history.pushState({ page: "app", [BACK_DEPTH_KEY]: 1 }, "", `${T1}?item=a`);
      window.history.pushState({ page: "app", [BACK_DEPTH_KEY]: 2 }, "", `${T1}?item=b`);
      startHistory(`${T1}?item=b`, "/trips");
      const push = vi.spyOn(window.history, "pushState");
      const onClose = vi.fn();
      const { unmount } = render(<View onClose={onClose} url={`${T1}?item=b`} />);
      await settle();
      expect(push).not.toHaveBeenCalled();
      expect(depth()).toBe(2);

      if (delta < 0) {
        await navigate(delta);
        expect(onClose).toHaveBeenCalledTimes(1);
      }
      unmount();
      await settle();
      expect(depth()).toBe(0);
      expect(url()).toBe("/trips");
    },
  );

  it("lets only the URL's view take over its entry", async () => {
    window.history.replaceState({ page: "app" }, "", `${T1}?item=a`);
    startHistory(`${T1}?item=a`, "/trips");
    const other = "/trips/7c2e9d40-1b3a-4f6e-8d5c-a9e1f0b2c3d4";
    const { unmount } = render(<View onClose={vi.fn()} url={other} />);
    await settle();
    expect(depth()).toBe(2);
    expect(url()).toBe(other);

    unmount();
    await settle();
    expect(depth()).toBe(1);
    await navigate(-1);
  });

  it("lets no view take over the entry once the user has moved on", async () => {
    window.history.replaceState({ page: "app" }, "", `${T1}?item=a`);
    startHistory(`${T1}?item=a`, "/trips");
    await navigate(-1);
    // Forward again: no view owns that entry, so it is left again at once, as any closed view's.
    await navigate(1);
    expect(depth()).toBe(0);
    expect(url()).toBe("/trips");

    const push = vi.spyOn(window.history, "pushState");
    const { unmount } = render(<View onClose={vi.fn()} url={`${T1}?item=a`} />);
    await settle();
    expect(push).toHaveBeenCalledTimes(1);
    expect(depth()).toBe(1);
    unmount();
    await settle();
  });
});

// A page with a gallery, as TripsPage: it opens the gallery for an entry whose URL names it. The
// view over the gallery (the evidence view) opens on its own button, after the gallery.
function Page({ galleryUrl }: { galleryUrl: string }) {
  const [open, setOpen] = useState(false);
  const [innerOpen, setInnerOpen] = useState(false);
  useHistoryChange(() => {
    if (url() === galleryUrl) setOpen(true);
  });
  const closeGallery = () => {
    setOpen(false);
    setInnerOpen(false);
  };
  return (
    <>
      <button onClick={() => setOpen(true)}>open gallery</button>
      <button onClick={() => setInnerOpen(true)}>open inner</button>
      <button onClick={closeGallery}>close gallery</button>
      {open && (
        <Gallery
          url={galleryUrl}
          inner={innerOpen}
          onClose={() => {
            closed("gallery");
            closeGallery();
          }}
          onCloseInner={() => {
            closed("inner");
            setInnerOpen(false);
          }}
        />
      )}
    </>
  );
}

describe("forward onto a closed view's entry", () => {
  const T1 = "/trips/0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11?item=b";

  it.each([
    ["back", async () => navigate(-1)],
    [
      "its close button",
      async () => {
        fireEvent.click(screen.getByRole("button", { name: "close gallery" }));
        await settle();
      },
    ],
  ])("lets a page open the view named in its URL again after %s, on that entry", async (_how, close) => {
    render(<Page galleryUrl={T1} />);
    fireEvent.click(screen.getByRole("button", { name: "open gallery" }));
    await settle();
    expect(url()).toBe(T1);
    const length = window.history.length;

    await close();
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    expect(url()).toBe("/trips");

    const push = vi.spyOn(window.history, "pushState");
    const go = vi.spyOn(window.history, "go");
    await navigate(1);
    expect(screen.getByTestId("gallery")).toBeInTheDocument();
    expect(url()).toBe(T1);
    expect(depth()).toBe(1);
    // Only the forward itself: no entry left again, none pushed.
    expect(go.mock.calls).toEqual([[1]]);
    expect(push).not.toHaveBeenCalled();
    expect(window.history.length).toBe(length);

    await navigate(-1);
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    expect(url()).toBe("/trips");
  });

  it("still leaves the entry of a closed view without a URL", async () => {
    render(<Page galleryUrl={T1} />);
    fireEvent.click(screen.getByRole("button", { name: "open gallery" }));
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "open inner" }));
    await settle();
    expect(depth()).toBe(2);

    await navigate(-2);
    expect(closed.mock.calls).toEqual([["inner"], ["gallery"]]);
    expect(url()).toBe("/trips");

    // The gallery opens again on its own entry; the view that was over it does not come back.
    await navigate(2);
    expect(screen.getByTestId("gallery")).toBeInTheDocument();
    expect(depth()).toBe(1);
    expect(url()).toBe(T1);
    expect(screen.queryByTestId("view")).not.toBeInTheDocument();

    await navigate(-1);
    expect(url()).toBe("/trips");
  });
});

describe("page entries", () => {
  it("pushes a page entry per tab, once, and reports back after the views it passed closed", async () => {
    const seen = vi.fn();
    const off = onHistoryChange(() => seen(url(), closed.mock.calls.length));
    const length = window.history.length;
    pushPage("/albums");
    pushPage("/albums");
    expect(url()).toBe("/albums");
    expect(depth()).toBe(0);
    expect(window.history.length).toBe(length + 1);

    render(<Stack />);
    await openViews(1);
    expect(depth()).toBe(1);
    await navigate(-1);
    expect(seen.mock.calls).toEqual([["/albums", 1]]);

    cleanup();
    await navigate(-1);
    expect(seen.mock.calls).toEqual([["/albums", 1], ["/trips", 1]]);
    off();
    await navigate(1);
    expect(seen).toHaveBeenCalledTimes(2);
    await navigate(-1);
  });
});

