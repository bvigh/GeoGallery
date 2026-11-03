import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Video360 from "./Video360";

// jsdom has no WebGL. The rest of three.js runs for real.
const renders = vi.hoisted(() => ({ count: 0 }));
vi.mock("three", async (importOriginal) => {
  const actual = await importOriginal<typeof import("three")>();
  class FakeRenderer {
    domElement = document.createElement("canvas");
    setSize() {}
    render() {
      renders.count++;
    }
    dispose() {}
  }
  return { ...actual, WebGLRenderer: FakeRenderer };
});

// Video360 builds its <video> off the DOM; catch it on creation.
let video: HTMLVideoElement;
const createElement = document.createElement.bind(document);

beforeEach(() => {
  renders.count = 0;
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(document, "createElement").mockImplementation((tag: string, options?: ElementCreationOptions) => {
    const el = createElement(tag, options);
    if (tag === "video") video = el as HTMLVideoElement;
    return el;
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function decodeAudioOnly() {
  Object.defineProperty(video, "readyState", { value: 2 });
  act(() => {
    video.dispatchEvent(new Event("loadeddata"));
  });
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
});
