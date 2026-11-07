import { useState, useRef, useEffect, type RefObject } from "react";
import { MapContainer, TileLayer, Marker, Polyline, useMapEvents, useMap } from "react-leaflet";
import type { Album, GameRound, GuessResult, OrientationKeyframe, Trip, FilterState } from "@/types";
import { api, ApiError } from "@/api/client";
import { activeFilterCount, emptyFilters, filterParams } from "@/lib/filters";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";
import VideoDecodeNotice from "@/components/viewer/VideoDecodeNotice";
import { ZoomableImage } from "@/components/viewer/ZoomableImage";
import { useVideoDecodeCheck } from "@/lib/video";
import { FilterPanel } from "@/components/map/FilterPanel";
import { MiniMapOverlay } from "@/components/map/MiniMapOverlay";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";

// dd-mm-yyyy (a one-digit day or month too) → yyyy-mm-dd, or "" for anything else. An
// impossible date is "" as well: the backend would drop it without a word.
function parseDdMmYyyy(s: string): string {
  const m = s.match(/^(\d{1,2})-(\d{1,2})-(\d{4})$/);
  if (!m) return "";
  const [day, month, year] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const d = new Date(Date.UTC(year, month - 1, day));
  if (d.getUTCFullYear() !== year || d.getUTCMonth() !== month - 1 || d.getUTCDate() !== day) return "";
  return `${m[3]}-${m[2].padStart(2, "0")}-${m[1].padStart(2, "0")}`;
}

// Lengths of the day, month and year in dd-mm-yyyy.
const DATE_PARTS = [2, 2, 4];

// Formats the date field while typing or pasting at its end. A phone's number pad has no "-"
// key, so a dash follows a full day or month. A typed separator (- / . or space) ends a shorter
// one, and the dashes already there are kept, so no digit moves into the next part. While
// deleting, a trailing dash is dropped, or Backspace could never get past it.
function formatDateInput(raw: string, previous: string): string {
  const typing = raw.length > previous.length;
  const parts = [""];
  for (const ch of raw) {
    const i = parts.length - 1;
    if (/\d/.test(ch)) {
      if (parts[i].length < DATE_PARTS[i]) parts[i] += ch;
      else if (i < 2) parts.push(ch);
    } else if (/[-/. ]/.test(ch) && parts[i] !== "" && i < 2) {
      parts.push("");
    }
  }
  const last = parts.length - 1;
  const out = parts.join("-");
  if (typing && last < 2 && parts[last].length === DATE_PARTS[last]) return `${out}-`;
  return !typing && out.endsWith("-") ? out.slice(0, -1) : out;
}

// Digits and dashes only; another separator becomes a dash.
function cleanDateText(s: string): string {
  return s.replace(/[/. ]/g, "-").replace(/[^\d-]/g, "");
}

// Video controls run along the bottom edge, so the guess map moves up to keep them reachable.
function guessMapClass(mediaType: string): string | undefined {
  if (mediaType === "video_360") return "bottom-12 touch:bottom-14";
  if (mediaType.startsWith("video")) return "bottom-16";
  return undefined;
}

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
  const [roundKeyframes, setRoundKeyframes] = useState<OrientationKeyframe[]>([]);
  const videoCheck = useVideoDecodeCheck(round?.url ?? "");
  const [guess, setGuess] = useState<{ lat: number; lng: number } | null>(null);
  const [guessDate, setGuessDate] = useState("");
  const [result, setResult] = useState<GuessResult | null>(null);
  const [trips, setTrips] = useState<Trip[]>([]);
  const [albums, setAlbums] = useState<Album[]>([]);
  const [filters, setFilters] = useState<FilterState>(emptyFilters);
  // Why there is no round to play, shown above Start Round.
  const [notice, setNotice] = useState<string | null>(null);
  const [guessError, setGuessError] = useState<string | null>(null);
  // Set when Submit found a date it cannot read; cleared by the next edit.
  const [dateInvalid, setDateInvalid] = useState(false);
  const scoreCardRef = useRef<HTMLElement>(null);

  useEffect(() => {
    api.get<Trip[]>("/trips").then(setTrips);
    api.listAlbums().then(setAlbums).catch(() => {});
  }, []);

  const activeCount = activeFilterCount(filters);

  const changeFilters = (next: FilterState) => {
    setFilters(next);
    // The notice was about the previous filters.
    setNotice(null);
  };

  const startRound = async () => {
    let r: GameRound;
    try {
      r = await api.get<GameRound>("/game/round", filterParams(filters));
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        // With filters set, a 404 only says that nothing matches them.
        setNotice(activeCount > 0 ? "No geolocated media match these filters" : "No geolocated media yet");
        setState("idle");
        return;
      }
      throw e;
    }
    setNotice(null);
    setGuessError(null);
    setRound(r);
    setGuess(null);
    setGuessDate("");
    setDateInvalid(false);
    setResult(null);
    if (r.media_type === "video_360") {
      api.getKeyframes(r.id).then(setRoundKeyframes).catch(() => setRoundKeyframes([]));
    } else {
      setRoundKeyframes([]);
    }
    setState("playing");
  };

  const changeGuessDate = (el: HTMLInputElement) => {
    setDateInvalid(false);
    const caret = el.selectionStart ?? el.value.length;
    if (caret === el.value.length) {
      setGuessDate(formatDateInput(el.value, guessDate));
      return;
    }
    // An edit inside the text keeps it as typed: reformatting would move digits into the next
    // part (and the caret to the end). parseDdMmYyyy checks the result on Submit.
    const head = cleanDateText(el.value.slice(0, caret));
    const next = (head + cleanDateText(el.value.slice(caret))).slice(0, 10);
    if (next !== el.value) {
      // Rewritten here, not by React, which would put the caret at the end.
      el.value = next;
      el.setSelectionRange(head.length, head.length);
    }
    setGuessDate(next);
  };

  const submitGuess = async () => {
    if (!round || !guess) return;
    setGuessError(null);
    const date = parseDdMmYyyy(guessDate);
    // Sent as is, an unreadable date would silently score no time points.
    if (guessDate !== "" && !date) {
      setDateInvalid(true);
      return;
    }
    let res: GuessResult;
    try {
      res = await api.post<GuessResult>("/game/guess", {
        media_id: round.id,
        guess_lat: guess.lat,
        guess_lng: guess.lng,
        guess_date: date,
        max_dist_km: round.max_dist_km,
      });
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        // Deleted by its owner, or no longer shared with the player (album or membership gone).
        // The presigned URL keeps showing it for a while, so say why the round ends.
        setNotice("This photo is no longer available");
        setRound(null);
        setState("idle");
        return;
      }
      console.error("Failed to submit guess:", e);
      setGuessError("Could not submit guess");
      return;
    }
    setResult(res);
    setState("result");
  };

  return (
    <div className="relative flex h-full flex-col">
      {state === "idle" && (
        <>
          <FilterPanel
            trips={trips}
            albums={albums}
            filters={filters}
            onChange={changeFilters}
            activeCount={activeCount}
          />
          {/* The vertical padding keeps the text clear of the Filters button on short screens. */}
          <div className="m-auto px-6 py-16 text-center">
            <h1 className="mb-4 text-2xl font-bold">GeoGallery Game</h1>
            <p className="mb-6 text-muted-foreground">
              You'll be shown a photo or video from your gallery. Guess where and
              when it was taken.
            </p>
            {notice && (
              <p className="mb-6 text-sm font-medium">{notice}</p>
            )}
            <button
              className="rounded-lg bg-primary px-6 py-2 text-primary-foreground font-medium touch:min-h-11"
              onClick={startRound}
            >
              Start Round
            </button>
          </div>
        </>
      )}

      {state === "playing" && round && (
        <div className="relative h-full w-full overflow-hidden bg-black">
          {/* Full-screen media background */}
          {round.media_type === "video_360" ? (
            <Video360
              url={round.url}
              onClose={() => {}}
              contained
              initialYaw={round.initial_yaw}
              initialPitch={round.initial_pitch}
              initialFov={round.initial_fov}
              keyframes={roundKeyframes}
            />
          ) : round.media_type === "photo_360" ? (
            <Panorama360
              url={round.url}
              onClose={() => {}}
              contained
              initialYaw={round.initial_yaw}
              initialPitch={round.initial_pitch}
              initialFov={round.initial_fov}
            />
          ) : round.media_type.startsWith("video") ? (
            <>
              {/* contain, not cover: on a phone cover crops away much of the frame, and the clues with it. */}
              <video
                src={round.url}
                controls
                className="absolute inset-0 h-full w-full object-contain"
                {...videoCheck.handlers}
              />
              {videoCheck.unsupported && <VideoDecodeNotice />}
            </>
          ) : (
            <ZoomableImage src={round.url} alt="guess this" />
          )}

          {/* The overlay unmounts after each round, so every round starts with the small map. */}
          <MiniMapOverlay
            className={guessMapClass(round.media_type)}
            footer={
              <div className="flex flex-col gap-2">
                <div className="flex items-center gap-2 rounded-xl bg-black/70 px-3 py-2 backdrop-blur-sm touch:p-1.5">
                  {/* text-base on touch: iOS zooms into a focused field under 16px and stays zoomed. */}
                  <input
                    type="text"
                    inputMode="numeric"
                    autoComplete="off"
                    aria-label="Date taken (dd-mm-yyyy)"
                    placeholder="dd-mm-yyyy"
                    maxLength={10}
                    className="min-w-0 flex-1 rounded bg-white/10 px-2 py-1 text-sm text-white placeholder-white/50 focus:outline-none focus:ring-1 focus:ring-white/40 touch:min-h-11 touch:text-base"
                    value={guessDate}
                    aria-invalid={dateInvalid || undefined}
                    aria-describedby={dateInvalid ? "guess-date-error" : undefined}
                    onChange={(e) => changeGuessDate(e.target)}
                  />
                  <button
                    className="shrink-0 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11 touch:px-4"
                    disabled={!guess}
                    onClick={submitGuess}
                  >
                    Submit
                  </button>
                </div>
                {dateInvalid && (
                  <p
                    id="guess-date-error"
                    role="alert"
                    className="rounded-xl bg-black/70 px-3 py-2 text-sm text-red-300 backdrop-blur-sm"
                  >
                    Enter the date as dd-mm-yyyy, or leave it empty
                  </p>
                )}
                {guessError && (
                  <p role="alert" className="rounded-xl bg-black/70 px-3 py-2 text-sm text-red-300 backdrop-blur-sm">
                    {guessError}
                  </p>
                )}
              </div>
            }
          >
            <ClickMarker onPick={(lat, lng) => setGuess({ lat, lng })} />
            {guess && <Marker position={[guess.lat, guess.lng]} icon={guessIcon} />}
          </MiniMapOverlay>
        </div>
      )}

      {state === "result" && result && guess && (
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
              pathOptions={{ color: "#000000", weight: 2, dashArray: "6 8", opacity: 0.85 }}
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
                onClick={startRound}
              >
                Next Round
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
