import React from "react";
import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import type { MediaItem } from "../../types";
import { MediaMap } from "./MediaMap";

vi.mock("react-leaflet", () => ({
  MapContainer: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="map">{children}</div>
  ),
  TileLayer: () => null,
  Marker: ({ children, position }: { children: React.ReactNode; position: [number, number] }) => (
    <div data-testid="marker" data-lat={position[0]} data-lng={position[1]}>
      {children}
    </div>
  ),
  Popup: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  useMap: () => ({ addControl: vi.fn(), removeControl: vi.fn() }),
}));

vi.mock("@changey/react-leaflet-markercluster", () => ({
  default: React.forwardRef(({ children }: { children: React.ReactNode }, _ref) => (
    <div>{children}</div>
  )),
}));

vi.mock("leaflet", () => ({
  default: {
    divIcon: vi.fn(() => ({})),
    control: { zoom: vi.fn(() => ({ addTo: vi.fn(), remove: vi.fn() })) },
  },
}));

function makeItem(overrides: Partial<MediaItem> = {}): MediaItem {
  return {
    id: "1",
    filename: "photo.jpg",
    media_type: "photo",
    lat: 51.5,
    lng: -0.1,
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: "key",
    trip_id: null,
    ...overrides,
  };
}

describe("MediaMap", () => {
  it("renders without crashing when items is empty", () => {
    render(<MediaMap items={[]} />);
    expect(screen.getByTestId("map")).toBeInTheDocument();
  });

  it("renders a marker for each item with valid coordinates", () => {
    const items = [
      makeItem({ id: "1", lat: 51.5, lng: -0.1 }),
      makeItem({ id: "2", lat: 48.8, lng: 2.3 }),
    ];
    render(<MediaMap items={items} />);
    expect(screen.getAllByTestId("marker")).toHaveLength(2);
  });

  it("skips items with null location", () => {
    const items = [
      makeItem({ id: "1", lat: 51.5, lng: -0.1 }),
      makeItem({ id: "2", lat: null, lng: null }),
      makeItem({ id: "3", lat: 48.8, lng: null }),
    ];
    render(<MediaMap items={items} />);
    expect(screen.getAllByTestId("marker")).toHaveLength(1);
  });
});
