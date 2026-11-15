import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { CameraTimeZone } from "@/components/settings/CameraTimeZone";
import type { UserSettings } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { getSettings: vi.fn(), updateSettings: vi.fn() } };
});

vi.mock("@/lib/time", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/time")>();
  return { ...actual, browserTimeZone: () => "Europe/Budapest" };
});

const mockApi = api as unknown as {
  getSettings: ReturnType<typeof vi.fn>;
  updateSettings: ReturnType<typeof vi.fn>;
};

function settings(zone: string | null, server = "Europe/Warsaw"): UserSettings {
  return { camera_timezone: zone, defaults: { camera_timezone: server } };
}

const zoneSelect = () => screen.getByLabelText("Zone") as HTMLSelectElement;
const saveButton = () => screen.getByRole("button", { name: "Save" });

describe("CameraTimeZone", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.getSettings.mockResolvedValue(settings(null));
  });

  it("explains the setting and what it does to stored items", async () => {
    render(<CameraTimeZone />);
    expect(screen.getByRole("heading", { name: "Camera time zone" })).toBeInTheDocument();
    expect(screen.getByText(/only when the file does not say its own offset/)).toBeInTheDocument();
    // The owner can move stored times too, and a time changed by hand stays when retime runs.
    const stored = screen.getByText(/keep their times until you edit or shift them, or an administrator runs/);
    expect(stored).toHaveTextContent(
      "apart from files uploaded with a zone of their own and items whose time you changed by hand."
    );
    expect(await screen.findByText("Your browser is in Europe/Budapest.")).toBeInTheDocument();
  });

  it("shows the server's zone as the default", async () => {
    render(<CameraTimeZone />);
    expect(await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" })).toBeInTheDocument();
    expect(zoneSelect().value).toBe("");
    expect(saveButton()).toHaveAttribute("aria-disabled", "true");
  });

  it("names the server's own zone when CAMERA_TIMEZONE is unset", async () => {
    mockApi.getSettings.mockResolvedValue(settings(null, "server zone (unset)"));
    render(<CameraTimeZone />);
    expect(await screen.findByRole("option", { name: "Server default (the server's own zone)" })).toBeInTheDocument();
  });

  it("shows the stored zone, even one the browser does not list", async () => {
    mockApi.getSettings.mockResolvedValue(settings("Asia/Calcutta"));
    render(<CameraTimeZone />);
    await waitFor(() => expect(zoneSelect().value).toBe("Asia/Calcutta"));
  });

  it("saves a zone and says so", async () => {
    mockApi.updateSettings.mockResolvedValue(settings("Asia/Tokyo"));
    render(<CameraTimeZone />);
    await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" });

    await userEvent.selectOptions(zoneSelect(), "Asia/Tokyo");
    expect(saveButton()).toHaveAttribute("aria-disabled", "false");
    await userEvent.click(saveButton());

    expect(mockApi.updateSettings).toHaveBeenCalledWith({ camera_timezone: "Asia/Tokyo" });
    expect(await screen.findByRole("status")).toHaveTextContent("Saved.");
    expect(zoneSelect().value).toBe("Asia/Tokyo");
    expect(saveButton()).toHaveAttribute("aria-disabled", "true");
    expect(saveButton()).toHaveFocus();
  });

  it("returns to the server's zone with null", async () => {
    mockApi.getSettings.mockResolvedValue(settings("Asia/Tokyo"));
    mockApi.updateSettings.mockResolvedValue(settings(null));
    render(<CameraTimeZone />);
    await waitFor(() => expect(zoneSelect().value).toBe("Asia/Tokyo"));

    await userEvent.selectOptions(zoneSelect(), "");
    await userEvent.click(saveButton());

    expect(mockApi.updateSettings).toHaveBeenCalledWith({ camera_timezone: null });
    await waitFor(() => expect(zoneSelect().value).toBe(""));
  });

  it("does nothing when nothing changed", async () => {
    render(<CameraTimeZone />);
    await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" });
    await userEvent.click(saveButton());
    expect(mockApi.updateSettings).not.toHaveBeenCalled();
  });

  it("shows the server's reason for a refused zone, and keeps the choice", async () => {
    mockApi.updateSettings.mockRejectedValue(
      new ApiError("PATCH /me/settings failed: 400", 400, 'camera_timezone: unknown time zone "Asia/Tokyo"')
    );
    render(<CameraTimeZone />);
    await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" });
    await userEvent.selectOptions(zoneSelect(), "Asia/Tokyo");
    await userEvent.click(saveButton());

    expect(await screen.findByText('camera_timezone: unknown time zone "Asia/Tokyo"')).toBeInTheDocument();
    expect(zoneSelect().value).toBe("Asia/Tokyo");
    expect(screen.getByRole("status")).toHaveTextContent("");

    await userEvent.selectOptions(zoneSelect(), "UTC");
    expect(screen.queryByText(/unknown time zone/)).not.toBeInTheDocument();
  });

  it("says a save failed for other errors", async () => {
    mockApi.updateSettings.mockRejectedValue(new ApiError("PATCH /me/settings failed: 500", 500));
    render(<CameraTimeZone />);
    await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" });
    await userEvent.selectOptions(zoneSelect(), "UTC");
    await userEvent.click(saveButton());
    expect(await screen.findByText("Could not save your camera time zone.")).toBeInTheDocument();
  });

  it("offers to try again when the setting cannot be loaded", async () => {
    mockApi.getSettings.mockRejectedValueOnce(new Error("down"));
    render(<CameraTimeZone />);
    expect(await screen.findByText("Could not load your camera time zone.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Zone")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" })).toBeInTheDocument();
    expect(screen.queryByText("Could not load your camera time zone.")).not.toBeInTheDocument();
  });

  it("gives its controls 44 px on touch", async () => {
    render(<CameraTimeZone />);
    await screen.findByRole("option", { name: "Server default (Europe/Warsaw)" });
    expect(zoneSelect()).toHaveClass("touch:min-h-11");
    expect(saveButton()).toHaveClass("touch:min-h-11");
  });
});
