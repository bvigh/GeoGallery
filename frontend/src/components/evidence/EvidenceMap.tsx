import { useEffect, useMemo } from "react";
import {
  CircleMarker,
  MapContainer,
  Marker,
  Pane,
  Polyline,
  TileLayer,
  Tooltip,
  useMap,
  useMapEvents,
} from "react-leaflet";
import L from "leaflet";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";
import { TrackLayer } from "@/components/map/TrackLayer";
import { bracketKindChip, bracketPoints, contextPointKind, REMATCH_SEGMENT_LABEL } from "@/lib/evidence";
import { formatColor, nearestPoint, POINT_HIT_RADIUS_PX } from "@/lib/track";
import type { ProvenanceResponse } from "@/types";
import type { PickedPoint } from "@/components/evidence/PointCard";
import "leaflet/dist/leaflet.css";

type LatLng = [number, number];

const WORLD_CENTER: LatLng = [20, 0];
const WORLD_ZOOM = 2;
// A fit around one point would otherwise zoom in as far as the tiles go.
const FIT_MAX_ZOOM = 16;
// The bracket above the context track (450), under the markers (600).
const BRACKET_PANE = "bracket";
const BRACKET_PANE_Z_INDEX = "460";
const BRACKET_COLOR = "#111827";

const itemMarkerIcon = L.divIcon({
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#22c55e;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  className: "",
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

function storedOrEstimated({ item, placement }: ProvenanceResponse): LatLng | null {
  if (item.lat !== null && item.lng !== null) return [item.lat, item.lng];
  return placement.now ? [placement.now.lat, placement.now.lng] : null;
}

function nearestOutside({ placement }: ProvenanceResponse) {
  const outside = placement.nearest_outside;
  return outside ? [outside.before, outside.after].filter((point) => point !== null) : [];
}

// What the view is fitted to: the first of these that has anything, else the whole world.
function fitPositions(data: ProvenanceResponse): LatLng[] {
  const marker = storedOrEstimated(data);
  const main: LatLng[] = [
    ...(marker ? [marker] : []),
    ...bracketPoints(data.placement.now).map((point): LatLng => [point.lat, point.lng]),
    ...data.placement.others.map((other): LatLng => [other.lat, other.lng]),
  ];
  if (main.length > 0) return main;
  const outside = nearestOutside(data).map((point): LatLng => [point.lat, point.lng]);
  if (outside.length > 0) return outside;
  return data.context.points.map((point): LatLng => [point.lat, point.lng]);
}

function FitView({ data }: { data: ProvenanceResponse }) {
  const map = useMap();
  useEffect(() => {
    function show() {
      const positions = fitPositions(data);
      if (positions.length === 0) {
        map.setView(WORLD_CENTER, WORLD_ZOOM, { animate: false });
      } else {
        map.fitBounds(L.latLngBounds(positions), { padding: [32, 32], maxZoom: FIT_MAX_ZOOM, animate: false });
      }
    }
    // As in the gallery's map controller: fitBounds on a 0×0 map sets the zoom to infinity, so a map
    // mounted before it has a size waits for its first measurement.
    const sized = () => {
      const size = map.getSize();
      return size.x > 0 && size.y > 0;
    };
    if (sized()) {
      show();
      return;
    }
    const onResize = () => {
      if (!sized()) return;
      map.off("resize", onResize);
      show();
    };
    map.on("resize", onResize);
    return () => {
      map.off("resize", onResize);
    };
  }, [map, data]);
  return null;
}

function kindOfFormat(format: string): string {
  return contextPointKind(format === "exif" || format === "manual" ? "derived" : "recorded");
}

function secondsBetween(a: string, b: string | null): number | null {
  return b === null ? null : Math.abs(Date.parse(a) - Date.parse(b)) / 1000;
}

// Every point a click can land on. The brackets come first, so on a tie they win over the same row
// in the context track, which knows less about it.
function pickablePoints(data: ProvenanceResponse): PickedPoint[] {
  const { item, placement, context } = data;
  const brackets = bracketPoints(placement.now).map(
    (point): PickedPoint => ({
      format: point.format,
      source: point.source,
      t: point.t,
      lat: point.lat,
      lng: point.lng,
      accuracyM: point.accuracy_m,
      altitudeM: point.altitude_m,
      gapS: point.gap_s,
      kind: bracketKindChip(point),
    })
  );
  const others = placement.others.map(
    (other): PickedPoint => ({
      format: other.format,
      source: other.source,
      t: other.t,
      lat: other.lat,
      lng: other.lng,
      accuracyM: other.accuracy_m,
      gapS: other.gap_s,
      kind: kindOfFormat(other.format),
    })
  );
  const outside = nearestOutside(data).map(
    (point): PickedPoint => ({
      format: point.format,
      source: point.source,
      t: point.t,
      lat: point.lat,
      lng: point.lng,
      gapS: point.gap_s,
      kind: kindOfFormat(point.format),
    })
  );
  const track = context.points.flatMap((point): PickedPoint[] => {
    const source = context.sources[point.s];
    if (!source) return [];
    return [
      {
        format: source.format,
        source: source.label,
        t: point.t,
        lat: point.lat,
        lng: point.lng,
        accuracyM: point.acc,
        altitudeM: point.alt,
        gapS: secondsBetween(point.t, item.taken_at),
        kind: contextPointKind(source.kind),
      },
    ];
  });
  return [...brackets, ...others, ...outside, ...track];
}

// The same query the touch: variant uses, so the radius follows what the layout assumes.
function hitRadius(): number {
  const coarse = window.matchMedia?.("(pointer: coarse)").matches ?? false;
  return coarse ? POINT_HIT_RADIUS_PX.coarse : POINT_HIT_RADIUS_PX.mouse;
}

function PointPicker({
  data,
  inert,
  onPick,
}: {
  data: ProvenanceResponse;
  inert: boolean;
  onPick: (point: PickedPoint | null) => void;
}) {
  const points = useMemo(() => pickablePoints(data), [data]);
  const map = useMapEvents({
    click(e) {
      if (inert) return;
      const project = (point: PickedPoint) => map.latLngToContainerPoint([point.lat, point.lng]);
      onPick(nearestPoint(e.containerPoint, points, project, hitRadius()));
    },
  });
  return null;
}

interface EvidenceMapProps {
  data: ProvenanceResponse;
  // Showing a previous item while the next one loads: nothing here may be clicked.
  inert: boolean;
  onPick: (point: PickedPoint | null) => void;
}

export function EvidenceMap({ data, inert, onPick }: EvidenceMapProps) {
  const { placement, context } = data;
  const { now, others, state } = placement;
  const marker = storedOrEstimated(data);
  const brackets = bracketPoints(now);
  const pair: LatLng[] | null =
    now?.arithmetic.shape === "two_sided" && now.before && now.after
      ? [
          [now.before.lat, now.before.lng],
          [now.after.lat, now.after.lng],
        ]
      : null;

  return (
    // isolate: Leaflet's panes and controls (z-index up to 1000) stay inside the map. keyboard off:
    // once a click focuses the map, Leaflet would pan on the arrows and stop them reaching the view.
    <MapContainer center={WORLD_CENTER} zoom={WORLD_ZOOM} className="isolate h-full w-full" zoomControl keyboard={false}>
      {/* A new tab: a mis-tap on the credit must not navigate away from the app. */}
      <TileLayer
        attribution='&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a>'
        url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
      />
      <FitView data={data} />
      <PointPicker data={data} inert={inert} onPick={onPick} />
      <TrackLayer track={context} />
      <Pane name={BRACKET_PANE} style={{ zIndex: BRACKET_PANE_Z_INDEX }}>
        {others.map((other) => (
          <CircleMarker
            key={other.format}
            center={[other.lat, other.lng]}
            radius={4}
            interactive={false}
            pathOptions={{ color: "white", weight: 1, fillColor: formatColor(other.format), fillOpacity: 1 }}
          />
        ))}
        {nearestOutside(data).map((point) => (
          <CircleMarker
            key={point.t}
            center={[point.lat, point.lng]}
            radius={4}
            interactive={false}
            pathOptions={{ color: formatColor(point.format), weight: 2, fillOpacity: 0 }}
          />
        ))}
        {pair && (
          // Keyed by its points: a line that only moves leaves its tooltip at the old position.
          <Polyline
            key={pair.flat().join()}
            positions={pair}
            interactive={false}
            pathOptions={{ color: BRACKET_COLOR, weight: 6, opacity: 0.8 }}
          >
            {state === "stale" && (
              <Tooltip permanent direction="center">
                {REMATCH_SEGMENT_LABEL}
              </Tooltip>
            )}
          </Polyline>
        )}
        {brackets.map((point) => (
          <CircleMarker
            key={`${point.source} ${point.t}`}
            center={[point.lat, point.lng]}
            radius={7}
            interactive={false}
            pathOptions={{ color: BRACKET_COLOR, weight: 3, fillColor: "white", fillOpacity: 1 }}
          />
        ))}
      </Pane>
      {/* Not interactive: a click on it must still reach the map, to pick a point beneath it. Leaflet
          would otherwise leave it in the tab order as a button that does nothing. */}
      {marker && (
        <Marker
          position={marker}
          icon={itemMarkerIcon}
          zIndexOffset={TOP_MARKER_Z_INDEX_OFFSET}
          interactive={false}
          keyboard={false}
        />
      )}
    </MapContainer>
  );
}
