import { fireEvent, render } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { VideoSoundContext } from "@/lib/videoSound";
import { FullSizeVideo } from "./FullSizeVideo";

const URL = "https://s3.example/clip";

// The video under a video sound setting: known (true or false) or not yet (null).
function underSetting(startMuted: boolean | null) {
  return (
    <VideoSoundContext.Provider value={{ startMuted, report: () => {} }}>
      <FullSizeVideo src={URL} controls autoPlay />
    </VideoSoundContext.Provider>
  );
}

describe("FullSizeVideo", () => {
  it.each([true, false])("starts as the video sound setting says (muted: %s)", (startMuted) => {
    const { container } = render(underSetting(startMuted));
    expect(container.querySelector("video")!.muted).toBe(startMuted);
  });

  it("starts muted while the setting is not known", () => {
    const { container } = render(underSetting(null));
    expect(container.querySelector("video")!.muted).toBe(true);
  });

  it("leaves an open video alone when the setting loads or changes: M and its own controls decide", () => {
    const { container, rerender } = render(underSetting(null));
    const video = container.querySelector("video")!;
    rerender(underSetting(false));
    expect(video.muted).toBe(true);

    // The user turns the sound on, and the setting changes to muted elsewhere.
    video.muted = false;
    rerender(underSetting(true));
    expect(video.muted).toBe(false);
    expect(container.querySelector("video")).toBe(video);
  });

  it("gives the host its element, attributes and handlers", () => {
    const ref = createRef<HTMLVideoElement>();
    const onPlay = vi.fn();
    render(<FullSizeVideo ref={ref} src={URL} controls autoPlay className="h-full" onPlay={onPlay} />);
    const video = ref.current!;
    expect(video).toBeInstanceOf(HTMLVideoElement);
    expect(video).toHaveAttribute("src", URL);
    expect(video).toHaveAttribute("controls");
    expect(video.autoplay).toBe(true);
    expect(video).toHaveClass("h-full");
    fireEvent.play(video);
    expect(onPlay).toHaveBeenCalledOnce();
  });
});
