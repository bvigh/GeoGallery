import type { SyntheticEvent } from "react";
import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useVideoDecodeCheck, videoDecodeFailed } from "./video";

// jsdom never decodes anything, so the media state is set by hand.
function fakeVideo(state: { readyState?: number; videoWidth?: number; errorCode?: number }) {
  const video = document.createElement("video");
  Object.defineProperty(video, "readyState", { value: state.readyState ?? 0 });
  Object.defineProperty(video, "videoWidth", { value: state.videoWidth ?? 0 });
  Object.defineProperty(video, "error", {
    value: state.errorCode === undefined ? null : { code: state.errorCode },
  });
  return video;
}

function event(video: HTMLVideoElement) {
  return { currentTarget: video } as SyntheticEvent<HTMLVideoElement>;
}

describe("videoDecodeFailed", () => {
  it.each([
    ["nothing loaded yet", { readyState: 0 }, false],
    ["metadata only", { readyState: 1 }, false],
    ["audio plays without a picture", { readyState: 2 }, true],
    ["enough data without a picture", { readyState: 4 }, true],
    ["a decoded picture", { readyState: 4, videoWidth: 1920 }, false],
    ["a decode error", { errorCode: 3 }, true],
    ["an unsupported source", { errorCode: 4 }, true],
    ["a network error", { errorCode: 2 }, false],
    ["an aborted load", { errorCode: 1 }, false],
  ])("%s", (_name, state, want) => {
    expect(videoDecodeFailed(fakeVideo(state))).toBe(want);
  });
});

describe("useVideoDecodeCheck", () => {
  it("flags the src whose picture fails and starts the next src unflagged", () => {
    const { result, rerender } = renderHook(({ src }) => useVideoDecodeCheck(src), {
      initialProps: { src: "https://s3.example/a" },
    });
    expect(result.current.unsupported).toBe(false);

    act(() => result.current.handlers.onLoadedData(event(fakeVideo({ readyState: 2 }))));
    expect(result.current.unsupported).toBe(true);

    rerender({ src: "https://s3.example/b" });
    expect(result.current.unsupported).toBe(false);
  });

  it("stays unflagged while the picture decodes", () => {
    const { result } = renderHook(() => useVideoDecodeCheck("https://s3.example/a"));
    const ok = fakeVideo({ readyState: 4, videoWidth: 5760 });
    act(() => {
      result.current.handlers.onLoadedData(event(ok));
      result.current.handlers.onTimeUpdate(event(ok));
    });
    expect(result.current.unsupported).toBe(false);
  });

  it("flags a media error", () => {
    const { result } = renderHook(() => useVideoDecodeCheck("https://s3.example/a"));
    act(() => result.current.handlers.onError(event(fakeVideo({ errorCode: 4 }))));
    expect(result.current.unsupported).toBe(true);
  });
});
