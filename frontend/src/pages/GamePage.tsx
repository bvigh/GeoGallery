import { useState, useRef, useEffect } from "react";
import { MapContainer, TileLayer, Marker, Polyline, useMapEvents, useMap } from "react-leaflet";
import type { GameRound, GuessResult } from "../types";
import { api } from "../api/client";
import L from "leaflet";
import Panorama360 from "../components/viewer/Panorama360";
import Video360 from "../components/viewer/Video360";

function ClickMarker({
  onPick,
}: {
  onPick: (lat: number, lng: number) => void;
}) {
  useMapEvents({
    click(e) {
      onPick(e.latlng.lat, e.latlng.lng);
    },
  });
  return null;
}

function FitBounds({
  guess,
  actual,
}: {
  guess: { lat: number; lng: number };
  actual: { lat: number; lng: number };
}) {
  const map = useMap();
  useEffect(() => {
    const bounds = L.latLngBounds(
      [guess.lat, guess.lng],
      [actual.lat, actual.lng],
    );
    map.fitBounds(bounds, { padding: [80, 80] });
  }, [map, guess, actual]);
  return null;
}

const guessIcon = L.divIcon({
  className: "",
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#f97316;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

const actualIcon = L.divIcon({
  className: "",
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#22c55e;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

type GameState = "idle" | "playing" | "result";

export function GamePage() {
  const [state, setState] = useState<GameState>("idle");
  const [round, setRound] = useState<GameRound | null>(null);
  const [guess, setGuess] = useState<{ lat: number; lng: number } | null>(null);
  const [guessDate, setGuessDate] = useState("");
  const [result, setResult] = useState<GuessResult | null>(null);
  const markerRef = useRef<L.Marker | null>(null);

  const startRound = async () => {
    const r = await api.get<GameRound>("/game/round");
    setRound(r);
    setGuess(null);
    setGuessDate("");
    setResult(null);
    setState("playing");
  };

  const submitGuess = async () => {
    if (!round || !guess) return;
    const res = await api.post<GuessResult>("/game/guess", {
      media_id: round.id,
      guess_lat: guess.lat,
      guess_lng: guess.lng,
      guess_date: guessDate,
    });
    setResult(res);
    setState("result");
  };

  return (
    <div className="flex h-full flex-col">
      {state === "idle" && (
        <div className="m-auto text-center">
          <h1 className="mb-4 text-2xl font-bold">GeoGallery Game</h1>
          <p className="mb-6 text-muted-foreground">
            You'll be shown a photo or video from your gallery. Guess where and
            when it was taken.
          </p>
          <button
            className="rounded-lg bg-primary px-6 py-2 text-primary-foreground font-medium"
            onClick={startRound}
          >
            Start Round
          </button>
        </div>
      )}

      {state === "playing" && round && (
        <div className="relative h-full w-full overflow-hidden bg-black">
          {/* Full-screen media background */}
          {round.media_type === "video_360" ? (
            <Video360 url={round.url} onClose={() => {}} contained />
          ) : round.media_type === "photo_360" ? (
            <Panorama360 url={round.url} onClose={() => {}} contained />
          ) : round.media_type.startsWith("video") ? (
            <video
              src={round.url}
              controls
              className="absolute inset-0 h-full w-full object-cover"
            />
          ) : (
            <img
              src={round.url}
              alt="guess this"
              className="absolute inset-0 h-full w-full object-contain"
            />
          )}

          {/* Bottom-right overlay: guess map + controls */}
          <div className="absolute bottom-4 right-4 z-[900] flex w-72 flex-col gap-2">
            <div className="h-48 overflow-hidden rounded-xl shadow-2xl ring-1 ring-white/20">
              <MapContainer center={[20, 0]} zoom={2} className="h-full w-full">
                <TileLayer
                  attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
                  url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
                />
                <ClickMarker onPick={(lat, lng) => setGuess({ lat, lng })} />
                {guess && (
                  <Marker position={[guess.lat, guess.lng]} ref={markerRef} />
                )}
              </MapContainer>
            </div>
            <div className="flex items-center gap-2 rounded-xl bg-black/70 px-3 py-2 backdrop-blur-sm">
              <input
                type="date"
                className="min-w-0 flex-1 rounded bg-white/10 px-2 py-1 text-sm text-white placeholder-white/50 focus:outline-none focus:ring-1 focus:ring-white/40"
                value={guessDate}
                onChange={(e) => setGuessDate(e.target.value)}
              />
              <button
                className="shrink-0 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
                disabled={!guess}
                onClick={submitGuess}
              >
                Submit
              </button>
            </div>
          </div>
        </div>
      )}

      {state === "result" && result && guess && (
        <div className="relative h-full w-full">
          <MapContainer
            center={[result.actual_lat, result.actual_lng]}
            zoom={5}
            className="h-full w-full"
            zoomControl={false}
          >
            <TileLayer
              attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
              url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
            />
            <FitBounds
              guess={guess}
              actual={{ lat: result.actual_lat, lng: result.actual_lng }}
            />
            <Marker position={[guess.lat, guess.lng]} icon={guessIcon} />
            <Marker
              position={[result.actual_lat, result.actual_lng]}
              icon={actualIcon}
            />
            <Polyline
              positions={[
                [guess.lat, guess.lng],
                [result.actual_lat, result.actual_lng],
              ]}
              pathOptions={{ color: "#000000", weight: 2, dashArray: "6 8", opacity: 0.85 }}
            />
          </MapContainer>

          {/* Score card overlay */}
          <div className="absolute bottom-6 left-1/2 z-[900] w-80 -translate-x-1/2 rounded-2xl bg-black/80 px-6 py-5 text-center text-white shadow-2xl backdrop-blur-sm ring-1 ring-white/10">
            <p className="text-4xl font-bold text-primary">
              {Math.round(result.total_score).toLocaleString()} pts
            </p>
            <div className="mt-3 space-y-1 text-sm text-white/80">
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
            <div className="mt-3 flex justify-center gap-4 text-xs text-white/60">
              <span className="flex items-center gap-1.5">
                <span className="inline-block h-3 w-3 rounded-full bg-orange-500" /> Your guess
              </span>
              <span className="flex items-center gap-1.5">
                <span className="inline-block h-3 w-3 rounded-full bg-green-500" /> Actual location
              </span>
            </div>
            <button
              className="mt-4 w-full rounded-lg bg-primary py-2 font-medium text-primary-foreground"
              onClick={startRound}
            >
              Next Round
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
