import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Marker, useMap, useMapEvents } from "react-leaflet";
import "leaflet/dist/leaflet.css";
import type { GameRound, OrientationKeyframe } from "@/types";
import { api } from "@/api/client";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";
import { FullSizeVideo } from "@/components/viewer/FullSizeVideo";
import VideoDecodeNotice from "@/components/viewer/VideoDecodeNotice";
import { ZoomableImage } from "@/components/viewer/ZoomableImage";
import { useVideoDecodeCheck } from "@/lib/video";
import { useVideoKeys } from "@/lib/videoKeys";
import { MiniMapOverlay } from "@/components/map/MiniMapOverlay";
import { guessIcon } from "@/components/game/gameMarkers";

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

const GUESS_MAP_PADDING = 12;

// Opens the guess map on the region of the filtered media. Before paint, so the world view never
// flashes. Only once per round: the player's own zooming and panning stand.
function FitGuessMap({ bounds }: { bounds: NonNullable<GameRound["bounds"]> }) {
  const map = useMap();
  const [south, west, north, east] = bounds;

  useLayoutEffect(() => {
    const { x, y } = map.getSize();
    // Leaflet throws on the NaN zoom of a map with no room left inside the padding.
    if (x <= 2 * GUESS_MAP_PADDING || y <= 2 * GUESS_MAP_PADDING) return;
    map.fitBounds(
      [
        [south, west],
        [north, east],
      ],
      { padding: [GUESS_MAP_PADDING, GUESS_MAP_PADDING], animate: false },
    );
  }, [map, south, west, north, east]);
  return null;
}

export interface Guess {
  lat: number;
  lng: number;
}

interface Props {
  round: GameRound;
  // Called with the guess and its date (yyyy-mm-dd, or "" when the player gave none). The round
  // stays as it is until the host shows something else, so a failed submit can be sent again.
  onSubmit: (guess: Guess, date: string) => void;
  // Why the last submit failed; shown under the date field.
  error?: string | null;
}

// One round being played: the item full screen, and the guess map with the date field and Submit
// over it. Used by a free round and by a challenge's round. The host gives each round its own key,
// so every round starts with no guess, an empty date and the small map.
export function RoundPlay({ round, onSubmit, error = null }: Props) {
  const [keyframes, setKeyframes] = useState<OrientationKeyframe[]>([]);
  const videoCheck = useVideoDecodeCheck(round.url);
  const videoRef = useRef<HTMLVideoElement>(null);
  const videoKeys = useVideoKeys(videoRef);
  const [guess, setGuess] = useState<Guess | null>(null);
  const [guessDate, setGuessDate] = useState("");
  // Set when Submit found a date it cannot read; cleared by the next edit.
  const [dateInvalid, setDateInvalid] = useState(false);

  useEffect(() => {
    if (round.media_type !== "video_360") return;
    let current = true;
    api
      .getKeyframes(round.id)
      .then((list) => current && setKeyframes(list))
      .catch(() => current && setKeyframes([]));
    return () => {
      current = false;
    };
  }, [round.id, round.media_type]);

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

  const submit = () => {
    if (!guess) return;
    const date = parseDdMmYyyy(guessDate);
    // Sent as is, an unreadable date would silently score no time points.
    if (guessDate !== "" && !date) {
      setDateInvalid(true);
      return;
    }
    onSubmit(guess, date);
  };

  return (
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
          keyframes={keyframes}
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
          <FullSizeVideo
            ref={videoRef}
            src={round.url}
            controls
            className="absolute inset-0 h-full w-full object-contain"
            {...videoCheck.handlers}
            {...videoKeys}
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
                onClick={submit}
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
            {error && (
              <p role="alert" className="rounded-xl bg-black/70 px-3 py-2 text-sm text-red-300 backdrop-blur-sm">
                {error}
              </p>
            )}
          </div>
        }
      >
        {round.bounds && <FitGuessMap bounds={round.bounds} />}
        <ClickMarker onPick={(lat, lng) => setGuess({ lat, lng })} />
        {guess && <Marker position={[guess.lat, guess.lng]} icon={guessIcon} />}
      </MiniMapOverlay>
    </div>
  );
}
