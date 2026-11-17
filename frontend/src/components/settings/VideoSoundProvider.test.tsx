import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useContext } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/api/client";
import { VideoSoundContext, useVideosStartMuted } from "@/lib/videoSound";
import type { UserSettings } from "@/types";
import { VideoSoundProvider } from "./VideoSoundProvider";

vi.mock("@/api/client", () => ({ api: { getSettings: vi.fn() } }));

const mockApi = api as unknown as { getSettings: ReturnType<typeof vi.fn> };

function settings(muted: boolean | null): UserSettings {
  return {
    camera_timezone: null,
    videos_start_muted: muted,
    defaults: { camera_timezone: "UTC", videos_start_muted: false },
  };
}

// How a video would start, and a button that reports `next` as the Settings page does.
function Probe({ next }: { next: UserSettings }) {
  const startMuted = useVideosStartMuted();
  const { report } = useContext(VideoSoundContext);
  return (
    <>
      <p data-testid="start-muted">{String(startMuted)}</p>
      <button onClick={() => report(next)}>report</button>
    </>
  );
}

function renderProvider(next = settings(false)) {
  return render(
    <VideoSoundProvider>
      <Probe next={next} />
    </VideoSoundProvider>
  );
}

const startMuted = () => screen.getByTestId("start-muted").textContent;

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((res) => (resolve = res));
  return { promise, resolve };
}

describe("VideoSoundProvider", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it.each([
    ["the user's choice, with sound", settings(false), "false"],
    ["the user's choice, muted", settings(true), "true"],
    ["the server's default", settings(null), "false"],
  ])("loads the setting once, and videos start muted until it answers: %s", async (_name, answer, want) => {
    const load = deferred<UserSettings>();
    mockApi.getSettings.mockReturnValue(load.promise);
    renderProvider();
    expect(startMuted()).toBe("true");

    await act(async () => load.resolve(answer));
    expect(startMuted()).toBe(want);
    expect(mockApi.getSettings).toHaveBeenCalledTimes(1);
  });

  it("applies what the Settings page reports at once, over a first load that answers later", async () => {
    const load = deferred<UserSettings>();
    mockApi.getSettings.mockReturnValue(load.promise);
    renderProvider(settings(false));

    await userEvent.click(screen.getByRole("button", { name: "report" }));
    expect(startMuted()).toBe("false");
    await act(async () => load.resolve(settings(true)));
    expect(startMuted()).toBe("false");
  });

  it("starts videos muted when the setting cannot be loaded, until the Settings page reports it", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    mockApi.getSettings.mockRejectedValue(new Error("GET /me/settings failed: 500"));
    renderProvider(settings(false));
    await act(async () => {});
    expect(warn).toHaveBeenCalledWith("Could not load the video sound setting:", expect.any(Error));
    expect(startMuted()).toBe("true");

    await userEvent.click(screen.getByRole("button", { name: "report" }));
    expect(startMuted()).toBe("false");
    warn.mockRestore();
  });
});
