import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { VideoSound } from "@/components/settings/VideoSound";
import { VideoSoundContext } from "@/lib/videoSound";
import type { UserSettings } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { getSettings: vi.fn(), updateSettings: vi.fn() } };
});

const mockApi = api as unknown as {
  getSettings: ReturnType<typeof vi.fn>;
  updateSettings: ReturnType<typeof vi.fn>;
};

function settings(muted: boolean | null, defaultMuted = false): UserSettings {
  return {
    camera_timezone: "Asia/Tokyo",
    videos_start_muted: muted,
    defaults: { camera_timezone: "Europe/Warsaw", videos_start_muted: defaultMuted },
  };
}

const withSound = () => screen.getByRole("radio", { name: /^With sound/ });
const muted = () => screen.getByRole("radio", { name: /^Muted/ });
const saveButton = () => screen.getByRole("button", { name: "Save" });

// The section in the app, whose VideoSoundProvider hears what it loads and saves.
function renderSection() {
  const report = vi.fn();
  render(
    <VideoSoundContext.Provider value={{ startMuted: null, report }}>
      <VideoSound />
    </VideoSoundContext.Provider>
  );
  return { report };
}

describe("VideoSound", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.getSettings.mockResolvedValue(settings(null));
  });

  it("explains where the setting applies, that M still toggles, and what browsers allow", async () => {
    renderSection();
    expect(screen.getByRole("heading", { name: "Video sound" })).toBeInTheDocument();
    expect(
      screen.getByText(/when you open it full size: in a gallery, on the map, in a Game round and in a challenge's results/)
    ).toHaveTextContent("M, or the player's own button, turns the sound on and off while it plays.");
    expect(screen.getByText(/Browsers allow sound only once you have clicked or pressed a key on the page/)).toHaveTextContent(
      "a video opened from a link or after a reload may start muted, or wait for its play button."
    );
    await screen.findByRole("radiogroup", { name: "Videos start" });
  });

  it("starts on the server's default, marked as such, and tells the app what it loaded", async () => {
    const { report } = renderSection();
    expect(screen.getByText("Loading your settings…")).toBeInTheDocument();
    expect(await screen.findByRole("radio", { name: "With sound (default)" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Muted" })).not.toBeChecked();
    expect(saveButton()).toHaveAttribute("aria-disabled", "true");
    expect(report).toHaveBeenCalledWith(settings(null));
  });

  it.each([
    ["the user's choice", settings(true), "Muted"],
    ["a muted default", settings(null, true), "Muted (default)"],
    ["with sound over a muted default", settings(false, true), "With sound"],
  ])("checks %s", async (_name, answer, checked) => {
    mockApi.getSettings.mockResolvedValue(answer);
    renderSection();
    expect(await screen.findByRole("radio", { name: checked })).toBeChecked();
  });

  it("saves muted, says so, and tells the app", async () => {
    mockApi.updateSettings.mockResolvedValue(settings(true));
    const { report } = renderSection();
    await userEvent.click(await screen.findByRole("radio", { name: "Muted" }));
    expect(saveButton()).toHaveAttribute("aria-disabled", "false");
    await userEvent.click(saveButton());

    expect(mockApi.updateSettings).toHaveBeenCalledWith({ videos_start_muted: true });
    expect(await screen.findByRole("status")).toHaveTextContent("Saved.");
    expect(report).toHaveBeenLastCalledWith(settings(true));
    expect(muted()).toBeChecked();
    expect(saveButton()).toHaveAttribute("aria-disabled", "true");
    expect(saveButton()).toHaveFocus();
  });

  it("saves with sound as a choice of its own, not the default's null", async () => {
    mockApi.getSettings.mockResolvedValue(settings(true));
    mockApi.updateSettings.mockResolvedValue(settings(false));
    renderSection();
    await waitFor(() => expect(muted()).toBeChecked());

    await userEvent.click(withSound());
    await userEvent.click(saveButton());
    expect(mockApi.updateSettings).toHaveBeenCalledWith({ videos_start_muted: false });
    await waitFor(() => expect(withSound()).toBeChecked());
  });

  it("does nothing when nothing changed, and choosing back the stored value leaves nothing to save", async () => {
    renderSection();
    await screen.findByRole("radiogroup", { name: "Videos start" });
    await userEvent.click(saveButton());
    expect(mockApi.updateSettings).not.toHaveBeenCalled();

    await userEvent.click(muted());
    await userEvent.click(withSound());
    expect(saveButton()).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(saveButton());
    expect(mockApi.updateSettings).not.toHaveBeenCalled();
  });

  it("shows the server's reason for a refused value, and keeps the choice", async () => {
    mockApi.updateSettings.mockRejectedValue(
      new ApiError("PATCH /me/settings failed: 400", 400, "videos_start_muted: want true, false or null")
    );
    const { report } = renderSection();
    await userEvent.click(await screen.findByRole("radio", { name: "Muted" }));
    await userEvent.click(saveButton());

    expect(await screen.findByText("videos_start_muted: want true, false or null")).toBeInTheDocument();
    expect(muted()).toBeChecked();
    expect(screen.getByRole("status")).toHaveTextContent("");
    expect(report).toHaveBeenCalledTimes(1);

    await userEvent.click(withSound());
    expect(screen.queryByText(/want true, false or null/)).not.toBeInTheDocument();
  });

  it("says a save failed for other errors", async () => {
    mockApi.updateSettings.mockRejectedValue(new ApiError("PATCH /me/settings failed: 500", 500));
    renderSection();
    await userEvent.click(await screen.findByRole("radio", { name: "Muted" }));
    await userEvent.click(saveButton());
    expect(await screen.findByText("Could not save your video sound setting.")).toBeInTheDocument();
  });

  it("offers to try again when the setting cannot be loaded", async () => {
    mockApi.getSettings.mockRejectedValueOnce(new Error("down"));
    const { report } = renderSection();
    expect(await screen.findByText("Could not load your video sound setting.")).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(report).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("radio", { name: "With sound (default)" })).toBeChecked();
    expect(screen.queryByText("Could not load your video sound setting.")).not.toBeInTheDocument();
    expect(report).toHaveBeenCalledOnce();
  });

  it("gives its controls 44 px on touch", async () => {
    renderSection();
    await screen.findByRole("radiogroup", { name: "Videos start" });
    expect(withSound().closest("label")).toHaveClass("touch:min-h-11");
    expect(muted().closest("label")).toHaveClass("touch:min-h-11");
    expect(saveButton()).toHaveClass("touch:min-h-11");
  });
});
