import { useState, useRef } from "react";
import { MapContainer, TileLayer, Marker, useMapEvents } from "react-leaflet";
import type { GameRound, GuessResult } from "../types";
import { api } from "../api/client";
import L from "leaflet";

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
        <div className="flex h-full">
          <div className="w-1/2 border-r">
            {round.media_type.startsWith("video") ? (
              <video src={round.url} controls className="h-full w-full object-contain" />
            ) : (
              <img src={round.url} alt="guess this" className="h-full w-full object-contain" />
            )}
          </div>
          <div className="flex w-1/2 flex-col">
            <div className="flex-1">
              <MapContainer center={[20, 0]} zoom={2} className="h-full w-full">
                <TileLayer
                  attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
                  url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
                />
                <ClickMarker onPick={(lat, lng) => setGuess({ lat, lng })} />
                {guess && (
                  <Marker
                    position={[guess.lat, guess.lng]}
                    ref={markerRef}
                  />
                )}
              </MapContainer>
            </div>
            <div className="flex items-center gap-3 border-t p-3">
              <input
                type="date"
                className="rounded border px-2 py-1 text-sm"
                value={guessDate}
                onChange={(e) => setGuessDate(e.target.value)}
              />
              <button
                className="ml-auto rounded bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
                disabled={!guess}
                onClick={submitGuess}
              >
                Submit Guess
              </button>
            </div>
          </div>
        </div>
      )}

      {state === "result" && result && (
        <div className="m-auto max-w-md text-center">
          <h2 className="mb-2 text-xl font-bold">Result</h2>
          <p className="text-4xl font-bold text-primary">
            {Math.round(result.total_score).toLocaleString()} pts
          </p>
          <div className="mt-4 rounded-lg border p-4 text-left text-sm">
            <p>
              <span className="font-medium">Distance:</span>{" "}
              {result.distance_km.toFixed(1)} km away
            </p>
            <p>
              <span className="font-medium">Location score:</span>{" "}
              {Math.round(result.location_score).toLocaleString()}
            </p>
            {result.date_diff_days !== null && (
              <>
                <p>
                  <span className="font-medium">Date off by:</span>{" "}
                  {Math.round(result.date_diff_days)} days
                </p>
                <p>
                  <span className="font-medium">Time score:</span>{" "}
                  {Math.round(result.time_score ?? 0).toLocaleString()}
                </p>
              </>
            )}
            {result.actual_date && (
              <p className="mt-2 text-muted-foreground">
                Actual date: {result.actual_date.slice(0, 10)}
              </p>
            )}
          </div>
          <button
            className="mt-4 rounded-lg bg-primary px-6 py-2 text-primary-foreground font-medium"
            onClick={startRound}
          >
            Next Round
          </button>
        </div>
      )}
    </div>
  );
}
