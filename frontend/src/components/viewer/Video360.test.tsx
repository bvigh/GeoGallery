import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type { OrientationKeyframe } from "@/types";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import Video360 from "./Video360";
import { shortToCameraFov } from "./sphereMath";
import {
  fakeGL,
  manualFrames,
  pinch,
  pointer,
  rendered,
  renderedMesh,
  resetRendered,
  stubLayout,
  stubPointerCapture,
  wheel,
} from "./viewerTestUtils";

// jsdom has no WebGL. The rest of three.js runs for real.
vi.mock("three", async (importOriginal) => {
  const { withFakeRenderer } = await import("./viewerTestUtils");
  return withFakeRenderer(await importOriginal());
});

type Capture = React.MutableRefObject<(() => { yaw: number; pitch: number; fov: number }) | null>;
type Control = React.MutableRefObject<{ pause: () => void; seek: (t: number) => void; seekAndApply: (t: number) => void } | null>;

// Video360 builds its <video> off the DOM; catch it on creation.
let video: HTMLVideoElement;
const createElement = document.createElement.bind(document);
let play: Mock<(this: HTMLMediaElement) => Promise<void>>;
let pause: Mock<(this: HTMLMediaElement) => void>;
let size: { w: number; h: number };
let unstubCapture: () => void;

beforeEach(() => {
  resetRendered();
  size = { w: 800, h: 450 };
  stubLayout(size);
  unstubCapture = stubPointerCapture();
  // Like a real element: `paused` follows these and they fire their events.
  play = vi.fn(function (this: HTMLMediaElement) {
    Object.defineProperty(this, "paused", { value: false, configurable: true });
    this.dispatchEvent(new Event("play"));
    return Promise.resolve();
  });
  pause = vi.fn(function (this: HTMLMediaElement) {
    Object.defineProperty(this, "paused", { value: true, configurable: true });
    this.dispatchEvent(new Event("pause"));
  });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(play);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(pause);
  vi.spyOn(document, "createElement").mockImplementation((tag: string, options?: ElementCreationOptions) => {
    const el = createElement(tag, options);
    if (tag === "video") video = el as HTMLVideoElement;
    return el;
  });
});

afterEach(() => {
  // Unmount while the media and layout stubs are still in place.
  cleanup();
  vi.restoreAllMocks();
  unstubCapture();
});

function decodeAudioOnly() {
  Object.defineProperty(video, "readyState", { value: 2 });
  act(() => {
    video.dispatchEvent(new Event("loadeddata"));
  });
}

function renderVideo(props: Partial<React.ComponentProps<typeof Video360>> = {}) {
  const captureRef: Capture = { current: null };
  const utils = render(
    <Video360 url="https://s3.example/v360" onClose={() => {}} contained captureRef={captureRef} {...props} />
  );
  const canvas = utils.container.querySelector("canvas")!;
  // Autoplay has run; count only what the test does.
  play.mockClear();
  pause.mockClear();
  return { ...utils, canvas, capture: () => captureRef.current!() };
}

function tap(canvas: Element, pointerType: "touch" | "mouse" = "touch") {
  pointer(canvas, "pointerdown", 1, 300, 200, pointerType);
  pointer(canvas, "pointerup", 1, 302, 201, pointerType);
}

describe("Video360", () => {
  it("shows the decode notice when the picture never arrives", () => {
    render(<Video360 url="https://s3.example/hevc" onClose={() => {}} contained />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    decodeAudioOnly();
    expect(screen.getByRole("link", { name: "How to enable HEVC playback" })).toBeInTheDocument();
  });

  it("clears the notice for the next url", () => {
    const { rerender } = render(<Video360 url="https://s3.example/hevc" onClose={() => {}} />);
    decodeAudioOnly();
    expect(screen.getByRole("alert")).toBeInTheDocument();

    rerender(<Video360 url="https://s3.example/h264" onClose={() => {}} />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not show the notice while the picture decodes", () => {
    render(<Video360 url="https://s3.example/h264" onClose={() => {}} contained />);
    Object.defineProperty(video, "readyState", { value: 4 });
    Object.defineProperty(video, "videoWidth", { value: 5760 });
    act(() => {
      video.dispatchEvent(new Event("loadeddata"));
      video.dispatchEvent(new Event("timeupdate"));
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("flags a decode error on a video without audio", () => {
    render(<Video360 url="https://s3.example/silent-hevc" onClose={() => {}} contained />);
    Object.defineProperty(video, "error", { value: { code: 4 } });
    act(() => {
      video.dispatchEvent(new Event("error"));
    });
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  it("pauses and plays on a tap, with feedback in the middle", () => {
    const { canvas } = renderVideo();
    tap(canvas);
    expect(pause).toHaveBeenCalledOnce();
    expect(play).not.toHaveBeenCalled();
    expect(screen.getByTestId("playback-flash")).toHaveAttribute("data-kind", "pause");
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();

    tap(canvas);
    expect(play).toHaveBeenCalledOnce();
    expect(screen.getByTestId("playback-flash")).toHaveAttribute("data-kind", "play");
    expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
  });

  it("toggles on a mouse click too, also full screen", () => {
    const { container } = render(<Video360 url="https://s3.example/v360" onClose={() => {}} />);
    pause.mockClear();
    tap(container.querySelector("canvas")!, "mouse");
    expect(pause).toHaveBeenCalledOnce();
  });

  it("hides the feedback after a moment", () => {
    vi.useFakeTimers();
    try {
      const { canvas } = renderVideo();
      tap(canvas);
      expect(screen.getByTestId("playback-flash")).toBeInTheDocument();
      act(() => {
        vi.advanceTimersByTime(1000);
      });
      expect(screen.queryByTestId("playback-flash")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not toggle on a small mouse drag that a finger would still tap with", () => {
    const { canvas } = renderVideo();
    pointer(canvas, "pointerdown", 1, 300, 200, "mouse");
    pointer(canvas, "pointermove", 1, 306, 200, "mouse");
    pointer(canvas, "pointerup", 1, 306, 200, "mouse");
    expect(pause).not.toHaveBeenCalled();
    expect(screen.queryByTestId("playback-flash")).not.toBeInTheDocument();
  });

  it("leaves playback alone on a tap or click with tapToPlay off, as in the view editor", () => {
    const { canvas } = renderVideo({ tapToPlay: false });
    tap(canvas, "mouse");
    tap(canvas, "touch");
    expect(pause).not.toHaveBeenCalled();
    expect(play).not.toHaveBeenCalled();
    expect(screen.queryByTestId("playback-flash")).not.toBeInTheDocument();
    // The control bar and Space still play and pause.
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { code: "Space", cancelable: true }));
    });
    expect(pause).toHaveBeenCalledOnce();
  });

  it("leaves Space to a dialog open over it, wherever focus is", () => {
    renderVideo();
    render(
      <Dialog open>
        <DialogContent>
          <DialogTitle>Albums</DialogTitle>
          <DialogDescription>Pick some</DialogDescription>
        </DialogContent>
      </Dialog>
    );
    act(() => (document.activeElement as HTMLElement).blur());
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { code: "Space", cancelable: true }));
    });
    expect(pause).not.toHaveBeenCalled();
    expect(play).not.toHaveBeenCalled();
  });

  it("does not toggle on a drag", () => {
    const { canvas, capture } = renderVideo();
    pointer(canvas, "pointerdown", 1, 400, 200);
    pointer(canvas, "pointermove", 1, 350, 200);
    pointer(canvas, "pointerup", 1, 350, 200);
    expect(pause).not.toHaveBeenCalled();
    expect(play).not.toHaveBeenCalled();
    expect(screen.queryByTestId("playback-flash")).not.toBeInTheDocument();
    expect(Math.abs(capture().yaw)).toBeGreaterThan(0.05);
  });

  it("only zooms on a pinch: no toggle and no turn", () => {
    const { canvas, capture } = renderVideo({ initialFov: 75 });
    pinch(canvas, 400, 225, 100, 140);
    expect(pause).not.toHaveBeenCalled();
    expect(play).not.toHaveBeenCalled();
    const view = capture();
    expect(view.fov).toBeLessThan(75);
    expect(view.yaw).toBeCloseTo(0, 1);
    expect(view.pitch).toBeCloseTo(0, 6);
  });

  it("does not toggle when a pinch starts as a tap", () => {
    const { canvas } = renderVideo();
    pointer(canvas, "pointerdown", 1, 300, 200);
    pointer(canvas, "pointerdown", 2, 400, 200);
    pointer(canvas, "pointerup", 2, 400, 200);
    pointer(canvas, "pointerup", 1, 300, 200);
    expect(pause).not.toHaveBeenCalled();
  });

  it("keeps the Space key", () => {
    renderVideo();
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { code: "Space", cancelable: true }));
    });
    expect(pause).toHaveBeenCalledOnce();
  });

  it("plays from the control bar without feedback over the picture", async () => {
    renderVideo();
    await userEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(pause).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("playback-flash")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Unmute" }));
    expect(screen.getByRole("button", { name: "Mute" })).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Seek" })).toBeInTheDocument();
  });

  it("zooms with the wheel", () => {
    const { canvas, capture } = renderVideo({ initialFov: 75 });
    wheel(canvas, 100);
    expect(capture().fov).toBeGreaterThan(75);
  });

  it("reports the short-side FOV on a portrait phone", () => {
    size.w = 400;
    size.h = 800;
    const { capture } = renderVideo({ initialFov: 75 });
    expect(capture().fov).toBeCloseTo(75, 9);
    expect(rendered.camera!.fov).toBeCloseTo(shortToCameraFov(75, 0.5), 9);
  });

  it("applies keyframes in the short-side convention on a portrait phone", () => {
    size.w = 400;
    size.h = 800;
    const keyframes: OrientationKeyframe[] = [
      { time: 0, yaw: 0, pitch: 0, fov: 90 },
      { time: 4, yaw: 1, pitch: 0.2, fov: 50 },
    ];
    const controlRef: Control = { current: null };
    const { capture } = renderVideo({ initialFov: 75, keyframes, controlRef });
    // Playing from 0:00, the first frame already follows the first keyframe.
    expect(capture().fov).toBeCloseTo(90, 9);
    expect(rendered.camera!.fov).toBeCloseTo(shortToCameraFov(90, 0.5), 9);

    act(() => controlRef.current!.seekAndApply(2));
    const view = capture();
    expect(view.yaw).toBeCloseTo(0.5, 6);
    expect(view.pitch).toBeCloseTo(0.1, 6);
    expect(view.fov).toBeCloseTo(70, 9);
    expect(rendered.camera!.fov).toBeCloseTo(shortToCameraFov(70, 0.5), 9);
  });

  describe("resuming a video with keyframes", () => {
    const keyframes: OrientationKeyframe[] = [
      { time: 0, yaw: 0, pitch: 0, fov: 75 },
      { time: 10, yaw: 1, pitch: 0, fov: 75 },
    ];

    it.each(["a tap", "Space"])("follows the camera path at once after %s", (how) => {
      let now = 1000;
      vi.spyOn(performance, "now").mockImplementation(() => now);
      const frames = manualFrames();
      const { canvas, capture } = renderVideo({ keyframes });
      act(() => screen.getByRole("button", { name: "Pause" }).click());
      expect(capture().yaw).toBeCloseTo(0, 6);
      Object.defineProperty(video, "currentTime", { value: 5, writable: true, configurable: true });

      if (how === "a tap") tap(canvas);
      else
        act(() => {
          window.dispatchEvent(new KeyboardEvent("keydown", { code: "Space", cancelable: true }));
        });
      expect(play).toHaveBeenCalledOnce();

      // Well inside the 5 s a drag holds the camera, and past the 1 s blend back to the path.
      now += 1100;
      frames.run();
      expect(capture().yaw).toBeCloseTo(0.5, 6);
    });

    it("still holds the camera after a drag", () => {
      let now = 1000;
      vi.spyOn(performance, "now").mockImplementation(() => now);
      const frames = manualFrames();
      const { canvas, capture } = renderVideo({ keyframes });
      Object.defineProperty(video, "currentTime", { value: 5, writable: true, configurable: true });
      pointer(canvas, "pointerdown", 1, 400, 200);
      pointer(canvas, "pointermove", 1, 350, 200);
      pointer(canvas, "pointerup", 1, 350, 200);
      const dragged = capture().yaw;
      now += 1100;
      frames.run();
      expect(capture().yaw).toBeCloseTo(dragged, 6);
    });
  });

  it("does not re-upload the frame on every animation frame", () => {
    const frames = manualFrames();
    renderVideo();
    Object.defineProperty(video, "readyState", { value: 4 });
    Object.defineProperty(video, "videoWidth", { value: 5760 });
    Object.defineProperty(video, "videoHeight", { value: 2880 });
    act(() => screen.getByRole("button", { name: "Pause" }).click());
    const texture = renderedMesh().material.map!;
    const version = texture.version;
    for (let i = 0; i < 5; i++) frames.run();
    expect(rendered.count).toBeGreaterThan(5);
    // VideoTexture flags each newly presented frame itself; a paused video presents none.
    expect(texture.version).toBe(version);
  });

  describe("without WebGL", () => {
    it("says so full screen, loads nothing and still closes", async () => {
      fakeGL.fail = true;
      const onClose = vi.fn();
      const { container } = render(<Video360 url="https://s3.example/v360" onClose={onClose} />);
      expect(screen.getByRole("alert")).toHaveTextContent("This device cannot show 360° views (WebGL unavailable).");
      expect(container.querySelector("canvas")).toBeNull();
      expect(screen.queryByRole("slider", { name: "Seek" })).not.toBeInTheDocument();
      expect(video.getAttribute("src")).toBeNull();
      expect(play).not.toHaveBeenCalled();
      await userEvent.click(screen.getByRole("button", { name: "Close 360 view" }));
      expect(onClose).toHaveBeenCalledOnce();
    });

    it("says so inside its parent, which keeps working", async () => {
      fakeGL.fail = true;
      const next = vi.fn();
      render(
        <div className="relative">
          <Video360 url="https://s3.example/v360" onClose={() => {}} contained />
          <button type="button" onClick={next}>
            Next round
          </button>
        </div>
      );
      expect(screen.getByRole("alert")).toHaveTextContent("WebGL unavailable");
      await userEvent.click(screen.getByRole("button", { name: "Next round" }));
      expect(next).toHaveBeenCalledOnce();
    });
  });

  it("pauses and says so when the browser takes the context away, until it comes back", () => {
    const { canvas } = renderVideo();
    act(() => {
      canvas.dispatchEvent(new Event("webglcontextlost"));
    });
    expect(pause).toHaveBeenCalledOnce();
    expect(screen.getByRole("alert")).toHaveTextContent("lost its graphics context");
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();

    act(() => {
      canvas.dispatchEvent(new Event("webglcontextrestored"));
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("closes from the full-screen header", async () => {
    const onClose = vi.fn();
    render(<Video360 url="https://s3.example/v360" onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: "Close 360 view" }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("has no close button when contained", () => {
    renderVideo();
    expect(screen.queryByRole("button", { name: "Close 360 view" })).not.toBeInTheDocument();
  });
});
