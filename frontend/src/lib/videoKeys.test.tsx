import { useRef, type ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { stubMediaPlayback } from "@/components/viewer/viewerTestUtils";
import { useVideoKeys } from "@/lib/videoKeys";

function Player({
  enabled,
  showVideo = true,
  children,
}: {
  enabled?: boolean;
  showVideo?: boolean;
  children?: ReactNode;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const keys = useVideoKeys(ref, enabled);
  return (
    <>
      {showVideo && <video data-testid="video" ref={ref} tabIndex={0} {...keys} />}
      {children}
    </>
  );
}

const SPACE = { code: "Space", key: " " };
const M = { code: "KeyM", key: "m" };

function video() {
  return screen.getByTestId<HTMLVideoElement>("video");
}

describe("useVideoKeys", () => {
  let play: MockInstance;
  let pause: MockInstance;

  beforeEach(() => {
    ({ play, pause } = stubMediaPlayback());
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("toggles playback once per Space", () => {
    render(<Player />);
    fireEvent.keyDown(video(), SPACE);
    expect(play).toHaveBeenCalledOnce();
    expect(video().paused).toBe(false);

    fireEvent.keyDown(video(), SPACE);
    expect(pause).toHaveBeenCalledOnce();
    expect(play).toHaveBeenCalledOnce();
    expect(video().paused).toBe(true);
  });

  it("gets to a key on the video before the native controls do", () => {
    render(<Player />);
    const nativeControls = vi.fn();
    video().addEventListener("keydown", nativeControls);

    // fireEvent returns false when the event was cancelled.
    expect(fireEvent.keyDown(video(), SPACE)).toBe(false);
    expect(fireEvent.keyDown(video(), M)).toBe(false);
    expect(nativeControls).not.toHaveBeenCalled();
    expect(play).toHaveBeenCalledOnce();
  });

  it.each(["m", "M"])("toggles mute on %s", (key) => {
    render(<Player />);
    fireEvent.keyDown(video(), { key });
    expect(video().muted).toBe(true);
    fireEvent.keyDown(video(), { key });
    expect(video().muted).toBe(false);
  });

  describe("focus", () => {
    it.each([
      ["a button", <button key="c">Next</button>],
      ["a range slider", <input key="c" type="range" aria-label="target" />],
    ])("keeps both keys with focus on %s", (_name, control) => {
      const { container } = render(<Player>{control}</Player>);
      const target = container.querySelector("button, input")!;
      expect(fireEvent.keyDown(target, SPACE)).toBe(false);
      expect(play).toHaveBeenCalledOnce();
      fireEvent.keyDown(target, M);
      expect(video().muted).toBe(true);
    });

    it("keeps both keys with focus nowhere", () => {
      render(<Player />);
      fireEvent.keyDown(document.body, SPACE);
      fireEvent.keyDown(document.body, M);
      expect(play).toHaveBeenCalledOnce();
      expect(video().muted).toBe(true);
    });

    it.each([
      ["a text field", <input key="c" />],
      ["a checkbox", <input key="c" type="checkbox" />],
      ["a textarea", <textarea key="c" />],
      ["a select", <select key="c" />],
    ])("leaves both keys to %s", (_name, control) => {
      const { container } = render(<Player>{control}</Player>);
      const target = container.querySelector("input, textarea, select")!;
      expect(fireEvent.keyDown(target, SPACE)).toBe(true);
      expect(fireEvent.keyDown(target, M)).toBe(true);
      expect(play).not.toHaveBeenCalled();
      expect(video().muted).toBe(false);
    });

    it("leaves both keys to editable text", () => {
      render(
        <Player>
          <div data-testid="editable" />
        </Player>
      );
      const target = screen.getByTestId("editable");
      // jsdom does not implement isContentEditable.
      Object.defineProperty(target, "isContentEditable", { value: true });
      expect(fireEvent.keyDown(target, SPACE)).toBe(true);
      expect(fireEvent.keyDown(target, M)).toBe(true);
      expect(play).not.toHaveBeenCalled();
    });
  });

  it("toggles once while a key is held", () => {
    render(<Player />);
    fireEvent.keyDown(video(), SPACE);
    // Still cancelled, or the native controls would take the repeats.
    expect(fireEvent.keyDown(video(), { ...SPACE, repeat: true })).toBe(false);
    expect(play).toHaveBeenCalledOnce();
    expect(pause).not.toHaveBeenCalled();
    fireEvent.keyDown(video(), M);
    fireEvent.keyDown(video(), { ...M, repeat: true });
    expect(video().muted).toBe(true);
  });

  it.each(["ctrlKey", "metaKey", "altKey"])("leaves the key to the browser with %s", (modifier) => {
    render(<Player />);
    expect(fireEvent.keyDown(video(), { ...M, [modifier]: true })).toBe(true);
    expect(fireEvent.keyDown(video(), { ...SPACE, [modifier]: true })).toBe(true);
    expect(video().muted).toBe(false);
    expect(play).not.toHaveBeenCalled();
  });

  it("ignores other keys", () => {
    render(<Player />);
    expect(fireEvent.keyDown(video(), { code: "KeyK", key: "k" })).toBe(true);
    expect(play).not.toHaveBeenCalled();
  });

  it("leaves both keys to a dialog over the video, even with focus outside it", () => {
    render(
      <Player>
        <Dialog open>
          <DialogContent>
            <DialogTitle>Albums</DialogTitle>
            <DialogDescription>Pick some</DialogDescription>
          </DialogContent>
        </Dialog>
      </Player>
    );
    expect(fireEvent.keyDown(document.body, SPACE)).toBe(true);
    expect(fireEvent.keyDown(document.body, M)).toBe(true);
    expect(play).not.toHaveBeenCalled();
    expect(video().muted).toBe(false);
  });

  it("does not touch the keys without a video", () => {
    render(<Player showVideo={false} />);
    expect(fireEvent.keyDown(document.body, SPACE)).toBe(true);
    expect(fireEvent.keyDown(document.body, M)).toBe(true);
  });

  it("does nothing while disabled, and works again when enabled", () => {
    const { rerender } = render(<Player enabled={false} />);
    expect(fireEvent.keyDown(document.body, SPACE)).toBe(true);
    expect(play).not.toHaveBeenCalled();

    rerender(<Player enabled />);
    expect(fireEvent.keyDown(document.body, SPACE)).toBe(false);
    expect(play).toHaveBeenCalledOnce();
  });

  it("stops listening when unmounted", () => {
    const { unmount } = render(<Player />);
    unmount();
    expect(fireEvent.keyDown(document.body, SPACE)).toBe(true);
    expect(play).not.toHaveBeenCalled();
  });

  describe("focus left in the native controls", () => {
    const CHANGES = ["play", "pause", "seeked", "volumeChange"] as const;

    it.each(CHANGES)("moves off the video after a mouse caused %s", (change) => {
      render(<Player />);
      video().focus();
      fireEvent[change](video());
      expect(document.activeElement).toBe(document.body);
    });

    it.each(CHANGES)("stays on a control reached with Tab through %s", (change) => {
      render(<Player />);
      fireEvent.keyDown(document.body, { key: "Tab" });
      video().focus();
      fireEvent[change](video());
      expect(document.activeElement).toBe(video());
    });

    it("moves off again once the mouse is back after a Tab", () => {
      render(<Player />);
      fireEvent.keyDown(document.body, { key: "Tab" });
      video().focus();
      fireEvent.pointerMove(document.body);
      fireEvent.pause(video());
      expect(document.activeElement).toBe(document.body);
    });

    it("does not take focus from anything else", () => {
      render(
        <Player>
          <button>Next</button>
        </Player>
      );
      const next = screen.getByRole("button", { name: "Next" });
      next.focus();
      fireEvent.pause(video());
      expect(document.activeElement).toBe(next);
    });
  });
});
