import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api } from "@/api/client";
import { EditLocationModal } from "@/components/EditLocationModal";
import { BACK_DEPTH_KEY } from "@/lib/useCloseOnBack";

// How many fullscreen views hold a history entry (useCloseOnBack).
const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}
import type { MediaItem } from "@/types";

vi.mock("@/api/client", () => ({
  api: { updateLocation: vi.fn() },
}));

const mockApi = api as unknown as { updateLocation: ReturnType<typeof vi.fn> };

const item: MediaItem = {
  id: "m1",
  filename: "a-very-long-camera-file-name-PXL_20240701_101112345.jpg",
  media_type: "photo",
  lat: null,
  lng: null,
  gps_confidence: null,
  gps_source: null,
  taken_at: null,
  storage_key: "media/u1/abc.jpg",
  trip_id: null,
  owner_id: "u1",
  owner_name: "Alice",
};

describe("EditLocationModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.updateLocation.mockResolvedValue(undefined);
  });

  // A modal left open would pop its history entry after the next test began.
  afterEach(async () => {
    cleanup();
    await settle();
  });

  it("closes on the phone's back gesture", async () => {
    const onClose = vi.fn();
    render(<EditLocationModal item={item} onClose={onClose} onSaved={vi.fn()} />);
    await settle();
    expect(depth()).toBe(1);

    await act(async () => {
      const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
      window.history.back();
      await popped;
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // On the window: focus usually stays on the button behind the modal that opened it.
  it("closes on Escape, but not from the coordinates field", async () => {
    const onClose = vi.fn();
    render(<EditLocationModal item={item} onClose={onClose} onSaved={vi.fn()} />);

    await userEvent.type(screen.getByRole("textbox", { name: "Coordinates" }), "48.8{Escape}");
    expect(onClose).not.toHaveBeenCalled();

    act(() => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("opens the map credit in a new tab, so a mis-tap does not leave the app", () => {
    const { container } = render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={vi.fn()} />);
    const credit = container.querySelector<HTMLAnchorElement>('.leaflet-control-attribution a[href*="openstreetmap.org"]');
    expect(credit).not.toBeNull();
    expect(credit).toHaveAttribute("target", "_blank");
    expect(credit).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("keeps Leaflet's controls inside the map, under the bars", () => {
    const { container } = render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(container.querySelector(".leaflet-container")).toHaveClass("isolate");
  });

  it("closes from a named, finger-sized button", async () => {
    const onClose = vi.fn();
    render(<EditLocationModal item={item} onClose={onClose} onSaved={vi.fn()} />);
    const close = screen.getByRole("button", { name: "Close" });
    expect(close).toHaveClass("touch:h-11", "touch:w-11");
    await userEvent.click(close);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("truncates a long file name instead of widening the bar", () => {
    render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByText(/^Edit GPS location/)).toHaveClass("min-w-0", "truncate");
  });

  it("saves pasted coordinates", async () => {
    const onSaved = vi.fn();
    render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={onSaved} />);
    expect(screen.getByRole("button", { name: "Save location" })).toBeDisabled();

    const input = screen.getByRole("textbox", { name: "Coordinates" });
    expect(input).toHaveClass("touch:text-base");
    await userEvent.type(input, "48.8566, 2.3522");
    await userEvent.click(screen.getByRole("button", { name: "Go" }));
    expect(screen.getByText("48.856600, 2.352200")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save location" }));
    expect(mockApi.updateLocation).toHaveBeenCalledWith("m1", 48.8566, 2.3522);
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(48.8566, 2.3522));
  });

  // A portrait phone with the keyboard open is as short as a sideways one; short: excludes it
  // (tailwind.screens.test.ts), so the field keeps the whole width while coordinates are pasted.
  it("puts the bottom bar's rows side by side only on a sideways phone", () => {
    render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={vi.fn()} />);
    const inputRow = screen.getByRole("textbox", { name: "Coordinates" }).parentElement!;
    const bar = inputRow.parentElement!;
    expect(bar).toHaveClass("flex-col", "short:flex-row");
    expect(inputRow).toHaveClass("short:flex-1");
    const rowClasses = [...bar.classList, ...inputRow.classList].filter((c) => c.includes("flex-row"));
    expect(rowClasses).toEqual(["short:flex-row"]);
  });

  it("ignores coordinates it cannot parse", async () => {
    render(<EditLocationModal item={item} onClose={vi.fn()} onSaved={vi.fn()} />);
    await userEvent.type(screen.getByRole("textbox", { name: "Coordinates" }), "somewhere{Enter}");
    expect(screen.getByText("Click on the map to set location")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save location" })).toBeDisabled();
  });
});
