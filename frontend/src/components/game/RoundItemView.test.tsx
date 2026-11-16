import { useEffect, useRef, type ComponentProps } from "react";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/api/client";
import { RoundItemView } from "@/components/game/RoundItemView";
import { SPHERE_HINT } from "@/components/viewer/ViewerHeader";
import { stubMediaPlayback } from "@/components/viewer/viewerTestUtils";
import { ITEM_NOT_SHARED, type RoundItemState } from "@/lib/challenge";
import { BACK_DEPTH_KEY } from "@/lib/history";
import type { ChallengeRoundResult, OrientationKeyframe } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { getKeyframes: vi.fn() } };
});

interface SphereProps {
  url: string;
  contained?: boolean;
  initialYaw?: number | null;
  initialPitch?: number | null;
  initialFov?: number | null;
  keyframes?: OrientationKeyframe[];
}

// The 360° viewers need WebGL (their own tests have a fake renderer). Here they are stand-ins that
// record what the view gives them. The Video360 stand-in takes the focus as it starts, as the real
// one does with its picture.
const spheres = vi.hoisted(() => ({ panorama: null as SphereProps | null, video: null as SphereProps | null }));

vi.mock("@/components/viewer/Panorama360", () => ({
  default: (props: SphereProps) => {
    spheres.panorama = props;
    return <div data-testid="panorama" />;
  },
}));

vi.mock("@/components/viewer/Video360", () => ({
  default: function Video360Stub(props: SphereProps) {
    spheres.video = props;
    const ref = useRef<HTMLDivElement>(null);
    useEffect(() => ref.current?.focus(), []);
    return <div ref={ref} tabIndex={-1} data-testid="video360" data-keyframes={props.keyframes?.length ?? 0} />;
  },
}));

const mockApi = api as unknown as Record<"getKeyframes", ReturnType<typeof vi.fn>>;

const URL = "https://s3.example/media/abc";

function result(extra: Partial<ChallengeRoundResult> = {}): ChallengeRoundResult {
  return {
    position: 1,
    number: 1,
    available: true,
    media_id: "m1",
    media_type: "photo",
    url: URL,
    initial_yaw: null,
    initial_pitch: null,
    initial_fov: null,
    actual_lat: 47.5,
    actual_lng: 19,
    actual_date: "2024-05-02 12:00:00+00",
    guesses: [],
    ...extra,
  };
}

const shown = (r: ChallengeRoundResult): RoundItemState => ({ status: "shown", result: r });

type Props = ComponentProps<typeof RoundItemView>;

function renderView(props: Partial<Props> = {}) {
  const onClose = vi.fn();
  const onRetry = vi.fn();
  const view = render(
    <RoundItemView number={1} mediaType="photo" state={shown(result())} onRetry={onRetry} onClose={onClose} {...props} />,
  );
  return { ...view, onClose, onRetry };
}

const dialog = (name = "Round 1's photo") => screen.getByRole("dialog", { name });

const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

describe("RoundItemView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    spheres.panorama = null;
    spheres.video = null;
    window.history.replaceState(null, "", "/game/c1");
    mockApi.getKeyframes.mockResolvedValue([]);
  });

  afterEach(async () => {
    await settle();
    vi.restoreAllMocks();
  });

  describe("the item", () => {
    it("shows a photo full size and zoomable, with its title and how to zoom", () => {
      renderView({ number: 3, state: shown(result({ number: 3 })) });
      const view = dialog("Round 3's photo");
      const img = within(view).getByRole("img", { name: "Round 3's photo" });
      expect(img).toHaveAttribute("src", URL);
      // The thumbnail's request mode, so the browser can reuse its download.
      expect(img).toHaveAttribute("crossorigin", "anonymous");
      expect(img.parentElement).toHaveAttribute("data-zoomed", "false");
      expect(view).toHaveTextContent("Round 3's photo · pinch or scroll to zoom");
      expect(mockApi.getKeyframes).not.toHaveBeenCalled();
    });

    it("shows a 360° photo in the panorama viewer, on the round's default view", () => {
      renderView({
        mediaType: "photo_360",
        state: shown(result({ media_type: "photo_360", initial_yaw: 1.5, initial_pitch: -0.2, initial_fov: 70 })),
      });
      const view = dialog("Round 1's 360° photo");
      expect(within(view).getByTestId("panorama")).toBeInTheDocument();
      expect(spheres.panorama).toMatchObject({ url: URL, contained: true, initialYaw: 1.5, initialPitch: -0.2, initialFov: 70 });
      expect(view).toHaveTextContent(`Round 1's 360° photo · ${SPHERE_HINT}`);
      expect(mockApi.getKeyframes).not.toHaveBeenCalled();
    });

    it("opens a 360° photo without a view of its own on the viewer's default", () => {
      renderView({ mediaType: "photo_360", state: shown(result({ media_type: "photo_360" })) });
      expect(spheres.panorama).toMatchObject({ initialYaw: null, initialPitch: null, initialFov: null });
    });

    it("plays a 360° video on its owner's keyframes, read by the item's ID", async () => {
      const keyframes: OrientationKeyframe[] = [
        { time: 0, yaw: 0, pitch: 0, fov: 75 },
        { time: 4, yaw: 1, pitch: 0.1, fov: 60 },
      ];
      mockApi.getKeyframes.mockResolvedValue(keyframes);
      renderView({
        mediaType: "video_360",
        state: shown(result({ media_type: "video_360", media_id: "v9", initial_yaw: 0.4, initial_fov: 80 })),
      });
      const view = dialog("Round 1's 360° video");
      expect(spheres.video).toMatchObject({ url: URL, contained: true, initialYaw: 0.4, initialPitch: null, initialFov: 80 });
      // On its default view until the keyframes come.
      expect(within(view).getByTestId("video360")).toHaveAttribute("data-keyframes", "0");
      expect(mockApi.getKeyframes).toHaveBeenCalledWith("v9");
      expect(await within(view).findByTestId("video360")).toHaveAttribute("data-keyframes", "2");
      expect(spheres.video!.keyframes).toEqual(keyframes);
      expect(view).toHaveTextContent(`Round 1's 360° video · ${SPHERE_HINT}`);
    });

    it("plays a 360° video on its default view when its keyframes cannot be read", async () => {
      mockApi.getKeyframes.mockRejectedValue(new Error("network"));
      renderView({ mediaType: "video_360", state: shown(result({ media_type: "video_360" })) });
      await act(async () => {});
      expect(mockApi.getKeyframes).toHaveBeenCalledTimes(1);
      expect(spheres.video!.keyframes).toEqual([]);
    });

    it("plays a flat video with its controls, takes Space and M, and says when it cannot decode it", () => {
      const { play, pause } = stubMediaPlayback();
      renderView({ mediaType: "video", state: shown(result({ media_type: "video" })) });
      const view = dialog("Round 1's video");
      const video = view.querySelector("video")!;
      expect(video).toHaveAttribute("src", URL);
      expect(video).toHaveAttribute("controls");
      expect(video.autoplay).toBe(true);
      // No hint: a video's own controls say what they do.
      expect(within(view).getByText("Round 1's video").parentElement).toHaveTextContent(/^Round 1's video$/);

      fireEvent.keyDown(window, { key: " ", code: "Space" });
      expect(play).toHaveBeenCalledTimes(1);
      fireEvent.keyDown(window, { key: " ", code: "Space" });
      expect(pause).toHaveBeenCalledTimes(1);
      fireEvent.keyDown(window, { key: "m" });
      expect(video.muted).toBe(true);

      expect(within(view).queryByRole("alert")).toBeNull();
      Object.defineProperty(video, "readyState", { value: 2, configurable: true });
      Object.defineProperty(video, "videoWidth", { value: 0, configurable: true });
      fireEvent.loadedData(video);
      expect(within(view).getByRole("alert")).toHaveTextContent("This browser can't decode this video's picture.");
    });

    it("draws nothing of the answer or the guesses over the item: its title and Close alone", () => {
      renderView({ state: shown(result({ guesses: [] })) });
      const view = dialog();
      expect(within(view).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([
        "Close full-size view",
      ]);
      expect(view.textContent).toBe("Round 1's photo · pinch or scroll to zoom");
    });

    it("names the round as the fresh results number it", () => {
      renderView({
        number: 3,
        mediaType: "photo",
        state: shown(result({ position: 3, number: 2, media_type: "photo_360" })),
      });
      expect(dialog("Round 2's 360° photo")).toBeInTheDocument();
    });
  });

  describe("before and instead of the item", () => {
    it("says Loading while the challenge is fetched again, under the round's title", () => {
      renderView({ number: 2, mediaType: "video", state: { status: "loading" } });
      const view = dialog("Round 2's video");
      expect(view).toHaveTextContent("Round 2's videoLoading…");
      expect(view.querySelector("img, video")).toBeNull();
    });

    it("says when the fetch failed, tries again on request and keeps the focus in the view", async () => {
      const { onRetry } = renderView({ mediaType: "video_360", state: { status: "failed" } });
      const view = dialog("Round 1's 360° video");
      expect(within(view).getByRole("alert")).toHaveTextContent("Could not load the 360° video.");
      const retry = within(view).getByRole("button", { name: "Try again" });
      expect(retry.className.split(" ")).toContain("touch:min-h-11");

      await userEvent.click(retry);
      expect(onRetry).toHaveBeenCalledTimes(1);
      expect(document.activeElement).toBe(view);
    });

    it("says why the round can no longer be shown", () => {
      renderView({ state: { status: "gone", reason: ITEM_NOT_SHARED } });
      const view = dialog();
      expect(within(view).getByRole("status")).toHaveTextContent(ITEM_NOT_SHARED);
      expect(view.querySelector("img, video")).toBeNull();
    });
  });

  describe("keys, focus and history", () => {
    it("sits above the challenge view, which is z-[1000]", () => {
      renderView();
      expect(dialog().className.split(" ")).toEqual(expect.arrayContaining(["fixed", "inset-0", "z-[1100]"]));
    });

    it("closes on Escape and on its Close button", async () => {
      const { onClose } = renderView();
      fireEvent.keyDown(window, { key: "Escape" });
      expect(onClose).toHaveBeenCalledTimes(1);
      await userEvent.click(screen.getByRole("button", { name: "Close full-size view" }));
      expect(onClose).toHaveBeenCalledTimes(2);
    });

    it("leaves a key that belongs to a dialog over it alone", () => {
      const { onClose } = renderView();
      const over = document.createElement("div");
      over.setAttribute("data-app-dialog", "");
      over.setAttribute("data-state", "open");
      document.body.append(over);
      fireEvent.keyDown(window, { key: "Escape" });
      expect(onClose).not.toHaveBeenCalled();
      over.remove();

      // Radix's closing Escape, already handled.
      const handled = new KeyboardEvent("keydown", { key: "Escape", cancelable: true, bubbles: true });
      handled.preventDefault();
      window.dispatchEvent(handled);
      expect(onClose).not.toHaveBeenCalled();
    });

    it("keeps Tab inside the view, the timeline of a flat video included", () => {
      renderView({ mediaType: "video", state: shown(result({ media_type: "video" })) });
      const view = dialog("Round 1's video");
      const close = within(view).getByRole("button", { name: "Close full-size view" });
      // A browser focuses a video with controls (its timeline then takes the arrow keys); jsdom
      // cannot, so the call is what is checked.
      const focusVideo = vi.spyOn(view.querySelector("video")!, "focus");

      view.blur();
      fireEvent.keyDown(window, { key: "Tab" });
      expect(document.activeElement).toBe(close);
      fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
      expect(focusVideo).toHaveBeenCalledTimes(1);
    });

    it("takes the focus when it opens and gives it back to the button that opened it", () => {
      const opener = document.createElement("button");
      document.body.append(opener);
      opener.focus();
      const returnFocus = vi.fn();
      const { unmount } = renderView({ returnFocus });
      expect(document.activeElement).toBe(dialog());
      unmount();
      expect(document.activeElement).toBe(opener);
      expect(returnFocus).not.toHaveBeenCalled();
      opener.remove();
    });

    it("gives the focus back to the opener even when a 360° video took it as it started", () => {
      const opener = document.createElement("button");
      document.body.append(opener);
      opener.focus();
      const { unmount } = renderView({ mediaType: "video_360", state: shown(result({ media_type: "video_360" })) });
      expect(document.activeElement).toBe(screen.getByTestId("video360"));
      unmount();
      expect(document.activeElement).toBe(opener);
      opener.remove();
    });

    it("hands the focus to its host when the button that opened it is gone", () => {
      const opener = document.createElement("button");
      document.body.append(opener);
      opener.focus();
      const returnFocus = vi.fn();
      const { unmount } = renderView({ returnFocus });
      opener.remove();
      unmount();
      expect(returnFocus).toHaveBeenCalledTimes(1);
    });

    it("has a history entry while open, without a URL of its own: back closes it", async () => {
      const { onClose } = renderView();
      await settle();
      expect(depth()).toBe(1);
      expect(window.location.pathname).toBe("/game/c1");

      await act(async () => {
        const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
        window.history.back();
        await popped;
      });
      await settle();
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(depth()).toBe(0);
    });

    it("gives the Close button a 44 px target on touch", () => {
      renderView();
      const classes = screen.getByRole("button", { name: "Close full-size view" }).className.split(" ");
      expect(classes).toEqual(expect.arrayContaining(["touch:h-11", "touch:w-11"]));
    });
  });
});
