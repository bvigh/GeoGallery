import { useEffect, useRef, type RefObject } from "react";
import { MapContainer, Marker, Polyline, TileLayer, useMap } from "react-leaflet";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import type { GuessResult } from "@/types";
import { actualIcon, GUESS_LINE, guessIcon } from "@/components/game/gameMarkers";
import type { Guess } from "@/components/game/RoundPlay";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";

// Fits both markers into the part of the map the score card leaves free, and again after a
// resize (a phone turned sideways) until the player moves the map themselves.
function FitBounds({
  guess,
  actual,
  cardRef,
}: {
  guess: { lat: number; lng: number };
  actual: { lat: number; lng: number };
  cardRef: RefObject<HTMLElement>;
}) {
  const map = useMap();
  const { lat: guessLat, lng: guessLng } = guess;
  const { lat: actualLat, lng: actualLng } = actual;

  useEffect(() => {
    const bounds = L.latLngBounds([guessLat, guessLng], [actualLat, actualLng]);
    const fit = () => {
      const { x, y } = map.getSize();
      // 80px on a desktop; a phone has too little map to spare that much.
      const pad = Math.round(Math.min(80, Math.max(24, Math.min(x, y) * 0.1)));
      const card = cardRef.current?.getBoundingClientRect();
      const covered = card && card.height > 0 ? Math.max(0, map.getContainer().getBoundingClientRect().bottom - card.top) : 0;
      // Always leave some map to fit into, however tall the card.
      const bottom = Math.min(pad + covered, Math.max(pad, y * 0.6));
      map.fitBounds(bounds, { paddingTopLeft: [pad, pad], paddingBottomRight: [pad, bottom] });
    };
    fit();

    let moved = false;
    const onResize = () => {
      if (!moved) fit();
    };
    const onUserMove = () => {
      moved = true;
    };
    // DOM events, not Leaflet's zoomstart: that also fires, a frame later, for fitBounds itself.
    const container = map.getContainer();
    const userEvents = ["pointerdown", "wheel", "keydown"] as const;
    userEvents.forEach((type) => container.addEventListener(type, onUserMove));
    map.on("resize", onResize);
    return () => {
      userEvents.forEach((type) => container.removeEventListener(type, onUserMove));
      map.off("resize", onResize);
    };
  }, [map, guessLat, guessLng, actualLat, actualLng, cardRef]);
  return null;
}

interface Props {
  guess: Guess;
  result: GuessResult;
  // What the card's button says and does: the next round, or a challenge's results.
  nextLabel: string;
  onNext: () => void;
  // A line above the points, such as a challenge's "Round 2 of 5".
  heading?: string;
}

// One scored guess: the map with the guess and the actual location, and the score card over it.
// Used by a free round and by a challenge's round.
export function RoundResult({ guess, result, nextLabel, onNext, heading }: Props) {
  const scoreCardRef = useRef<HTMLElement>(null);

  return (
    <div className="relative h-full w-full">
      <MapContainer
        center={[result.actual_lat, result.actual_lng]}
        zoom={5}
        // isolate: Leaflet's z-index 1000 panes and controls stay under the score card.
        className="isolate h-full w-full"
        zoomControl={false}
      >
        {/* A new tab: a mis-tap on the credit must not navigate away from the round. */}
        <TileLayer
          attribution='&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a>'
          url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
        />
        <FitBounds
          guess={guess}
          actual={{ lat: result.actual_lat, lng: result.actual_lng }}
          cardRef={scoreCardRef}
        />
        <Marker position={[guess.lat, guess.lng]} icon={guessIcon} />
        <Marker
          position={[result.actual_lat, result.actual_lng]}
          icon={actualIcon}
          zIndexOffset={TOP_MARKER_Z_INDEX_OFFSET}
        />
        <Polyline
          positions={[
            [guess.lat, guess.lng],
            [result.actual_lat, result.actual_lng],
          ]}
          pathOptions={GUESS_LINE}
        />
      </MapContainer>

      {/* Score card overlay. A phone held sideways gets two columns, so the card stays low
          and leaves most of the map visible. */}
      <section
        ref={scoreCardRef}
        aria-label="Round result"
        className="absolute bottom-6 left-1/2 z-[900] w-[min(20rem,calc(100%-2rem))] -translate-x-1/2 rounded-2xl bg-black/80 px-6 py-5 text-center text-white shadow-2xl ring-1 ring-white/10 backdrop-blur-sm short:bottom-3 short:flex short:w-[min(34rem,calc(100%-2rem))] short:items-center short:gap-4 short:px-4 short:py-3 short:text-left"
      >
        <div className="short:min-w-0 short:flex-1">
          {heading && <p className="mb-1 text-xs font-medium uppercase tracking-wide text-white/60">{heading}</p>}
          <p className="text-4xl font-bold text-primary short:text-2xl">
            {Math.round(result.total_score).toLocaleString()} pts
          </p>
          <div className="mt-3 space-y-1 text-sm text-white/80 short:mt-1 short:space-y-0 short:text-xs">
            <p>{result.distance_km.toFixed(1)} km away &nbsp;·&nbsp; {Math.round(result.location_score).toLocaleString()} location pts</p>
            {result.date_diff_days !== null && (
              <p>
                {Math.round(result.date_diff_days)} days off &nbsp;·&nbsp; {Math.round(result.time_score ?? 0).toLocaleString()} time pts
              </p>
            )}
            {result.actual_date && (
              <p className="text-white/50">Actual date: {result.actual_date.slice(0, 10)}</p>
            )}
          </div>
        </div>
        <div className="short:w-40 short:shrink-0">
          <div className="mt-3 flex justify-center gap-4 text-xs text-white/60 short:mt-0 short:flex-col short:items-start short:gap-1">
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-3 w-3 rounded-full bg-orange-500" /> Your guess
            </span>
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-3 w-3 rounded-full bg-green-500" /> Actual location
            </span>
          </div>
          <button
            className="mt-4 w-full rounded-lg bg-primary py-2 font-medium text-primary-foreground touch:min-h-11 short:mt-2"
            onClick={onNext}
          >
            {nextLabel}
          </button>
        </div>
      </section>
    </div>
  );
}
