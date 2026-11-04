import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Panorama360 from "./Panorama360";
import { MAX_FOV, MIN_FOV, shortToCameraFov } from "./sphereMath";
import { fakeGL, pinch, pointer, rendered, resetRendered, stubLayout, stubPointerCapture, wheel } from "./viewerTestUtils";

// jsdom has no WebGL. The rest of three.js runs for real.
vi.mock("three", async (importOriginal) => {
  const { withFakeRenderer } = await import("./viewerTestUtils");
  return withFakeRenderer(await importOriginal());
});

type Capture = React.MutableRefObject<(() => { yaw: number; pitch: number; fov: number }) | null>;

let size: { w: number; h: number };
let unstubCapture: () => void;

beforeEach(() => {
  resetRendered();
  size = { w: 800, h: 450 };
  stubLayout(size);
  unstubCapture = stubPointerCapture();
});

afterEach(() => {
  // Unmount while the media and layout stubs are still in place.
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  unstubCapture();
});

function renderContained(props: Partial<React.ComponentProps<typeof Panorama360>> = {}) {
  const captureRef: Capture = { current: null };
  const utils = render(
    <Panorama360 url="https://s3.example/pano.jpg" onClose={() => {}} contained captureRef={captureRef} {...props} />
  );
  const canvas = utils.container.querySelector("canvas")!;
  return { ...utils, canvas, capture: () => captureRef.current!() };
}

describe("Panorama360", () => {
  it("renders the sphere and reports the initial view", () => {
    const { canvas, capture } = renderContained({ initialYaw: 1, initialPitch: 0.25, initialFov: 60 });
    expect(canvas).toBeInTheDocument();
    expect(rendered.count).toBeGreaterThan(0);
    const view = capture();
    expect(view.yaw).toBeCloseTo(1, 6);
    expect(view.pitch).toBeCloseTo(0.25, 6);
    expect(view.fov).toBeCloseTo(60, 6);
  });

  it("keeps a stored FOV as the camera's vertical FOV on a landscape screen", () => {
    const { capture } = renderContained({ initialFov: 75 });
    expect(rendered.camera!.fov).toBeCloseTo(75, 9);
    expect(capture().fov).toBeCloseTo(75, 9);
  });

  it("spans the stored FOV across the width of a portrait screen", () => {
    size.w = 400;
    size.h = 800;
    const { capture } = renderContained({ initialFov: 75 });
    expect(rendered.camera!.aspect).toBe(0.5);
    expect(rendered.camera!.fov).toBeCloseTo(shortToCameraFov(75, 0.5), 9);
    // A default view saved on the phone looks the same on a desktop.
    expect(capture().fov).toBeCloseTo(75, 9);
  });

  it("follows the window when the phone turns", () => {
    const { capture } = renderContained({ initialFov: 75 });
    size.w = 450;
    size.h = 800;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(rendered.camera!.aspect).toBeCloseTo(450 / 800, 9);
    expect(rendered.camera!.fov).toBeCloseTo(shortToCameraFov(75, 450 / 800), 9);
    expect(capture().fov).toBeCloseTo(75, 9);
  });

  it("follows its container through a ResizeObserver", () => {
    let notify = () => {};
    const observed: Element[] = [];
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          notify = cb;
        }
        observe(el: Element) {
          observed.push(el);
        }
        disconnect() {}
      }
    );
    const { container } = renderContained();
    expect(observed).toEqual([container.firstElementChild]);

    size.w = 300;
    size.h = 600;
    act(() => notify());
    expect(rendered.camera!.aspect).toBe(0.5);
  });

  it("ignores a size of zero while hidden", () => {
    renderContained();
    size.w = 0;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(rendered.camera!.aspect).toBeCloseTo(800 / 450, 9);
  });

  it("zooms with the wheel and stays in range", () => {
    const { canvas, capture } = renderContained({ initialFov: 75 });
    const e = wheel(canvas, -100);
    expect(e.defaultPrevented).toBe(true);
    expect(capture().fov).toBeLessThan(75);

    for (let i = 0; i < 50; i++) wheel(canvas, 500);
    expect(capture().fov).toBe(MAX_FOV);
    for (let i = 0; i < 50; i++) wheel(canvas, -500);
    expect(capture().fov).toBe(MIN_FOV);
  });

  it("zooms in with a two-finger pinch, keeping the picture under the fingers", () => {
    const { canvas, capture } = renderContained({ initialFov: 75 });
    pinch(canvas, 400, 225, 100, 200);
    const fov = capture().fov;
    // Fingers twice as far apart magnify by two.
    expect(Math.tan((75 * Math.PI) / 360) / Math.tan((fov * Math.PI) / 360)).toBeCloseTo(2, 6);
  });

  it("zooms out with a pinch on a portrait phone, beyond the old 100° limit", () => {
    size.w = 400;
    size.h = 800;
    const { canvas, capture } = renderContained({ initialFov: 75 });
    pinch(canvas, 200, 400, 300, 50);
    expect(capture().fov).toBeGreaterThan(100);
    expect(rendered.camera!.fov).toBeLessThanOrEqual(140 + 1e-9);
  });

  it("does not turn the view when a second finger lands or lifts", () => {
    const { canvas, capture } = renderContained();
    pointer(canvas, "pointerdown", 1, 100, 200);
    pointer(canvas, "pointerdown", 2, 300, 200);
    pointer(canvas, "pointermove", 2, 300, 200);
    pointer(canvas, "pointerup", 2, 300, 200);
    pointer(canvas, "pointermove", 1, 100, 200);
    pointer(canvas, "pointerup", 1, 100, 200);
    expect(capture().yaw).toBeCloseTo(0, 6);
    expect(capture().pitch).toBeCloseTo(0, 6);
  });

  it("turns the view with a one-finger drag", () => {
    const { canvas, capture } = renderContained();
    pointer(canvas, "pointerdown", 1, 400, 200);
    pointer(canvas, "pointermove", 1, 300, 200);
    pointer(canvas, "pointerup", 1, 300, 200);
    expect(Math.abs(capture().yaw)).toBeGreaterThan(0.1);
  });

  it("has no close button when contained", () => {
    renderContained();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("closes from the full-screen header", async () => {
    const onClose = vi.fn();
    render(<Panorama360 url="https://s3.example/pano.jpg" onClose={onClose} />);
    expect(screen.getByText(/pinch or scroll to zoom/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Close 360 view" }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("removes the canvas, releases the context and stops reporting on unmount", () => {
    const captureRef: Capture = { current: null };
    const { container, unmount } = render(
      <Panorama360 url="https://s3.example/pano.jpg" onClose={() => {}} contained captureRef={captureRef} />
    );
    const mount = container.firstElementChild!;
    unmount();
    expect(mount.querySelector("canvas")).toBeNull();
    expect(captureRef.current).toBeNull();
    expect(rendered.log).toEqual(["renderer.dispose", "renderer.forceContextLoss"]);
  });

  describe("without WebGL", () => {
    it("says so full screen, and still closes", async () => {
      fakeGL.fail = true;
      const onClose = vi.fn();
      const { container } = render(<Panorama360 url="https://s3.example/pano.jpg" onClose={onClose} />);
      expect(screen.getByRole("alert")).toHaveTextContent("This device cannot show 360° views (WebGL unavailable).");
      expect(container.querySelector("canvas")).toBeNull();
      await userEvent.click(screen.getByRole("button", { name: "Close 360 view" }));
      expect(onClose).toHaveBeenCalledOnce();
    });

    it("says so inside its parent, which keeps working", async () => {
      fakeGL.fail = true;
      const captureRef: Capture = { current: null };
      const next = vi.fn();
      render(
        <div className="relative">
          <Panorama360 url="https://s3.example/pano.jpg" onClose={() => {}} contained captureRef={captureRef} />
          <button type="button" onClick={next}>
            Next round
          </button>
        </div>
      );
      expect(screen.getByRole("alert")).toHaveTextContent("WebGL unavailable");
      expect(captureRef.current).toBeNull();
      await userEvent.click(screen.getByRole("button", { name: "Next round" }));
      expect(next).toHaveBeenCalledOnce();
    });

    it("tries again for the next photo", () => {
      fakeGL.fail = true;
      const { container, rerender } = render(<Panorama360 url="https://s3.example/a.jpg" onClose={() => {}} contained />);
      expect(screen.getByRole("alert")).toBeInTheDocument();

      fakeGL.fail = false;
      rerender(<Panorama360 url="https://s3.example/b.jpg" onClose={() => {}} contained />);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(container.querySelector("canvas")).toBeInTheDocument();
    });
  });

  it("says when the browser takes the context away, until it comes back", () => {
    const { canvas } = renderContained();
    act(() => {
      canvas.dispatchEvent(new Event("webglcontextlost"));
    });
    expect(screen.getByRole("alert")).toHaveTextContent("lost its graphics context");
    act(() => {
      canvas.dispatchEvent(new Event("webglcontextrestored"));
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
