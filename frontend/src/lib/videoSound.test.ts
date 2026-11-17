import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { withVideoSound } from "@/components/viewer/viewerTestUtils";
import type { UserSettings } from "@/types";
import { startVideo, useVideosStartMuted, videosStartMuted } from "./videoSound";

function settings(muted: boolean | null, defaultMuted = false): UserSettings {
  return {
    camera_timezone: null,
    videos_start_muted: muted,
    defaults: { camera_timezone: "UTC", videos_start_muted: defaultMuted },
  };
}

describe("videosStartMuted", () => {
  it.each([
    ["the user's choice, muted", settings(true), true],
    ["the user's choice, with sound, over a muted default", settings(false, true), false],
    ["the server's default, with sound", settings(null), false],
    ["the server's default, muted", settings(null, true), true],
  ])("follows %s", (_name, s, want) => {
    expect(videosStartMuted(s)).toBe(want);
  });

  it("starts videos with sound for a backend without the setting", () => {
    const older = { camera_timezone: null, defaults: { camera_timezone: "UTC" } } as unknown as UserSettings;
    expect(videosStartMuted(older)).toBe(false);
  });
});

describe("useVideosStartMuted", () => {
  it.each([true, false])("gives the setting once it is known: %s", (muted) => {
    const { result } = renderHook(useVideosStartMuted, { wrapper: withVideoSound(muted) });
    expect(result.current).toBe(muted);
  });

  it("starts videos muted while nothing is known", () => {
    const { result } = renderHook(useVideosStartMuted);
    expect(result.current).toBe(true);
  });
});

describe("startVideo", () => {
  const refused = () => new DOMException("play() needs a user gesture to play with sound", "NotAllowedError");

  // A video whose play() answers as `answer` says, given how muted the video is when asked.
  function videoAnswering(answer: (muted: boolean) => Promise<void>) {
    const video = document.createElement("video");
    const asked: boolean[] = [];
    video.play = vi.fn(() => {
      asked.push(video.muted);
      return answer(video.muted);
    });
    return { video, asked };
  }

  it.each([true, false])("plays the video as asked (muted: %s)", async (muted) => {
    const { video, asked } = videoAnswering(() => Promise.resolve());
    await expect(startVideo(video, muted)).resolves.toBe(muted);
    expect(asked).toEqual([muted]);
    expect(video.muted).toBe(muted);
  });

  it("plays the video muted when the browser allows no sound yet", async () => {
    const { video, asked } = videoAnswering((muted) => (muted ? Promise.resolve() : Promise.reject(refused())));
    await expect(startVideo(video, false)).resolves.toBe(true);
    expect(asked).toEqual([false, true]);
    expect(video.muted).toBe(true);
  });

  it("leaves the video muted, waiting for its play button, when the browser refuses even that", async () => {
    const { video, asked } = videoAnswering(() => Promise.reject(refused()));
    await expect(startVideo(video, false)).resolves.toBe(true);
    expect(asked).toEqual([false, true]);
  });

  it("asks once for a muted video the browser refuses", async () => {
    const { video, asked } = videoAnswering(() => Promise.reject(refused()));
    await expect(startVideo(video, true)).resolves.toBe(true);
    expect(asked).toEqual([true]);
  });

  it.each([
    ["a pause before it played", new DOMException("The play() request was interrupted by a call to pause()", "AbortError")],
    ["a file it cannot play", new DOMException("The element has no supported sources.", "NotSupportedError")],
  ])("keeps the sound on %s", async (_name, error) => {
    const { video, asked } = videoAnswering(() => Promise.reject(error));
    await expect(startVideo(video, false)).resolves.toBe(false);
    expect(asked).toEqual([false]);
    expect(video.muted).toBe(false);
  });

  it("answers what the video is when it played: M pressed meanwhile counts", async () => {
    let played: () => void = () => {};
    const { video } = videoAnswering(() => new Promise((resolve) => (played = resolve)));
    const started = startVideo(video, true);
    video.muted = false;
    played();
    await expect(started).resolves.toBe(false);
  });
});
