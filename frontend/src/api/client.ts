import type {
  AdminUser,
  AgreementResponse,
  Album,
  AlbumMember,
  ApiToken,
  CameraClockRule,
  CameraClockRuleInput,
  Challenge,
  ChallengeSummary,
  ChecksResponse,
  CreatedApiToken,
  CurrentUser,
  DirectoryUser,
  FileReread,
  GpsUploadResult,
  GuessResult,
  LibraryCamera,
  MediaItem,
  OrientationKeyframe,
  ProvenanceResponse,
  SetTakenAtResult,
  ShiftTimesResult,
  TakenAtZone,
  UploadResponse,
  UserSettings,
  UserSettingsPatch,
} from "@/types";

// What to add to or remove from an album: explicit media, or a trip's current media.
export type AlbumMediaSelection = { mediaIds: string[] } | { tripId: string };

const BASE = "/api";

export class ApiError extends Error {
  // reason: the plain-text body of a 400 or 409 from a route whose reasons are written for the user, else null.
  constructor(message: string, public status: number, public reason: string | null = null) {
    super(message);
    this.name = "ApiError";
  }
}

function loginUrl(): string {
  const returnTo = window.location.pathname + window.location.search;
  return "/api/auth/login?return_to=" + encodeURIComponent(returnTo);
}

async function request(
  path: string,
  init: RequestInit = {},
  { redirectOn401 = true }: { redirectOn401?: boolean } = {}
): Promise<Response> {
  const res = await fetch(`${BASE}${path}`, init);
  if (res.status === 401 && redirectOn401) {
    window.location.assign(loginUrl());
  }
  return res;
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

type QueryParams = Record<string, string | string[]>;

// An array repeats its key; an empty string leaves the key out.
function withQuery(path: string, params?: QueryParams): string {
  if (!params) return path;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (Array.isArray(v)) {
      v.forEach((s) => qs.append(k, s));
    } else if (v !== "") {
      qs.set(k, v);
    }
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

async function get<T>(path: string, params?: QueryParams): Promise<T> {
  const res = await request(withQuery(path, params));
  if (!res.ok) throw new ApiError(`GET ${path} failed: ${res.status}`, res.status);
  return res.json();
}

// The largest page GET /api/media serves (the backend's maxListLimit).
const MEDIA_PAGE_MAX = 5000;

// Every item GET /api/media lists under these params (filters as filterParams builds them, a
// trip or an album), in its order: newest first, undated last. A page names the next one's
// cursor in X-Next-Cursor; the last page has none. onPage gets each page as it arrives. The
// signal stops the list: the request in flight is aborted and no later page is asked for.
async function listMedia(
  params: QueryParams,
  { onPage, signal }: { onPage?: (page: MediaItem[]) => void; signal?: AbortSignal } = {}
): Promise<MediaItem[]> {
  const aborted = () => new DOMException("The media list was aborted", "AbortError");
  const all: MediaItem[] = [];
  const query: QueryParams = { ...params, limit: String(MEDIA_PAGE_MAX) };
  for (;;) {
    const res = await request(withQuery("/media", query), { signal });
    if (!res.ok) throw new ApiError(`GET /media failed: ${res.status}`, res.status);
    const page: MediaItem[] = (await res.json()) ?? [];
    if (signal?.aborted) throw aborted();
    all.push(...page);
    onPage?.(page);
    const cursor = res.headers.get("X-Next-Cursor");
    if (!cursor) return all;
    if (signal?.aborted) throw aborted();
    query.cursor = cursor;
  }
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await request(path, jsonInit("POST", body));
  if (!res.ok) throw new ApiError(`POST ${path} failed: ${res.status}`, res.status);
  return res.json();
}

async function patch<T>(path: string, body: unknown): Promise<T> {
  const res = await request(path, jsonInit("PATCH", body));
  if (!res.ok) throw new ApiError(`PATCH ${path} failed: ${res.status}`, res.status);
  return res.json();
}

async function del(path: string): Promise<void> {
  const res = await request(path, { method: "DELETE" });
  if (!res.ok) throw new ApiError(`DELETE ${path} failed: ${res.status}`, res.status);
}

// For routes that answer 204 or a body the caller does not need.
async function send(method: string, path: string, body: unknown): Promise<void> {
  const res = await request(path, jsonInit(method, body));
  if (!res.ok) throw new ApiError(`${method} ${path} failed: ${res.status}`, res.status);
}

// Go encodes a nil slice as null; list helpers always resolve to an array.
async function getList<T>(path: string): Promise<T[]> {
  return (await get<T[] | null>(path)) ?? [];
}

async function getMediaUrl(id: string): Promise<string> {
  const data = await get<{ url: string }>(`/media/${id}/url`);
  return data.url;
}

async function getKeyframes(id: string): Promise<OrientationKeyframe[]> {
  return get<OrientationKeyframe[]>(`/media/${id}/keyframes`);
}

async function updateViewSettings(
  id: string,
  settings: { initial_yaw: number | null; initial_pitch: number | null; initial_fov: number | null }
): Promise<void> {
  await request(`/media/${id}/view-settings`, jsonInit("PATCH", settings));
}

async function replaceKeyframes(id: string, keyframes: OrientationKeyframe[]): Promise<void> {
  await request(`/media/${id}/keyframes`, jsonInit("PUT", keyframes));
}

async function updateLocation(id: string, lat: number, lng: number): Promise<void> {
  await request(`/media/${id}/location`, jsonInit("PATCH", { lat, lng }));
}

async function me(): Promise<CurrentUser | null> {
  const res = await request("/auth/me", {}, { redirectOn401: false });
  if (res.status === 401) return null;
  if (!res.ok) throw new ApiError(`GET /auth/me failed: ${res.status}`, res.status);
  return res.json();
}

async function logout(): Promise<void> {
  const res = await request("/auth/logout", { method: "POST" }, { redirectOn401: false });
  if (!res.ok) throw new ApiError(`POST /auth/logout failed: ${res.status}`, res.status);
  const data: { redirect_url: string } = await res.json();
  window.location.assign(data.redirect_url);
}

// album_id is a query param, not a form field, so the backend checks membership
// before it reads the (up to 4 GiB) body.
// cameraTimezone names the zone the files' camera clocks show, for this upload only; left out, the
// caller's setting or the server's applies.
async function upload(form: FormData, albumId?: string, cameraTimezone?: string): Promise<UploadResponse> {
  const path = withQuery("/media/upload", { album_id: albumId ?? "", camera_timezone: cameraTimezone ?? "" });
  const res = await request(path, { method: "POST", body: form });
  if (!res.ok) throw new ApiError(`POST /media/upload failed: ${res.status}`, res.status);
  return res.json();
}

// Which of these file hashes (src/lib/mediaHash.ts, at most 5000) the caller's own library holds:
// hash → media ID.
async function knownHashes(hashes: string[]): Promise<Record<string, string>> {
  const data = await post<{ known: Record<string, string> }>("/media/known-hashes", { hashes });
  return data.known;
}

// Track files in any format the backend reads (GPX, TCX, KML, NMEA, ...), as one multipart request.
async function uploadGpsTracks(files: File[]): Promise<GpsUploadResult> {
  const form = new FormData();
  for (const file of files) form.append("files", file);
  const res = await request("/gps-tracks/upload", { method: "POST", body: form });
  if (!res.ok) throw new ApiError(`POST /gps-tracks/upload failed: ${res.status}`, res.status);
  return res.json();
}

function listTokens(): Promise<ApiToken[]> {
  return get<ApiToken[]>("/tokens");
}

function createToken(name: string, expiresInDays: number): Promise<CreatedApiToken> {
  return post<CreatedApiToken>("/tokens", { name, expires_in_days: expiresInDays });
}

function revokeToken(id: string): Promise<void> {
  return del(`/tokens/${id}`);
}

function adminListUsers(): Promise<AdminUser[]> {
  return get<AdminUser[]>("/admin/users");
}

async function adminSetDisabled(id: string, disabled: boolean): Promise<void> {
  const res = await request(`/admin/users/${id}`, jsonInit("PATCH", { disabled }));
  if (!res.ok) throw new ApiError(`PATCH /admin/users/${id} failed: ${res.status}`, res.status);
}

async function adminCreateInvite(role: "user" | "admin"): Promise<string> {
  const data = await post<{ url: string }>("/admin/invites", { role });
  return data.url;
}

function listAlbums(): Promise<Album[]> {
  return getList<Album>("/albums");
}

function createAlbum(name: string): Promise<Album> {
  return post<Album>("/albums", { name });
}

function renameAlbum(id: string, name: string): Promise<void> {
  return send("PATCH", `/albums/${id}`, { name });
}

function deleteAlbum(id: string): Promise<void> {
  return del(`/albums/${id}`);
}

function listAlbumMembers(id: string): Promise<AlbumMember[]> {
  return getList<AlbumMember>(`/albums/${id}/members`);
}

function addAlbumMember(id: string, userId: string): Promise<void> {
  return send("POST", `/albums/${id}/members`, { user_id: userId });
}

// Removing yourself is leaving the album.
function removeAlbumMember(id: string, userId: string): Promise<void> {
  return del(`/albums/${id}/members/${userId}`);
}

function albumMediaBody(selection: AlbumMediaSelection) {
  return "tripId" in selection ? { trip_id: selection.tripId } : { media_ids: selection.mediaIds };
}

function addToAlbum(id: string, selection: AlbumMediaSelection): Promise<{ added: number }> {
  return post<{ added: number }>(`/albums/${id}/media`, albumMediaBody(selection));
}

function removeFromAlbum(id: string, selection: AlbumMediaSelection): Promise<{ removed: number }> {
  return post<{ removed: number }>(`/albums/${id}/media/remove`, albumMediaBody(selection));
}

// Albums the caller belongs to that contain this item.
function mediaAlbums(mediaId: string): Promise<Pick<Album, "id" | "name">[]> {
  return getList<Pick<Album, "id" | "name">>(`/media/${mediaId}/albums`);
}

function listUsers(): Promise<DirectoryUser[]> {
  return getList<DirectoryUser>("/users");
}

function mediaProvenance(id: string): Promise<ProvenanceResponse> {
  return get<ProvenanceResponse>(`/media/${id}/provenance`);
}

// The signal cancels the request when the item changes: for a video it stops an ffprobe process.
async function mediaProvenanceFile(id: string, signal: AbortSignal): Promise<FileReread> {
  const path = `/media/${id}/provenance/file`;
  const res = await request(path, { signal });
  if (!res.ok) throw new ApiError(`GET ${path} failed: ${res.status}`, res.status);
  return res.json();
}

function listCameraClockRules(): Promise<CameraClockRule[]> {
  return getList<CameraClockRule>("/camera-clock-rules");
}

// The plain-text body of a refusal whose status is one of `statuses`, for a route that writes its
// reasons for the user; null for any other status, or an empty or unreadable body.
async function refusalReason(res: Response, statuses: number[]): Promise<string | null> {
  if (!statuses.includes(res.status)) return null;
  try {
    return (await res.text()).trim() || null;
  } catch {
    return null;
  }
}

// A JSON request to a route that says why it refused a body: a 400's reason (and those of the
// other `reasonStatuses`) goes into ApiError.reason.
async function sendWithReason<T>(method: string, path: string, body: unknown, reasonStatuses = [400]): Promise<T> {
  const res = await request(path, jsonInit(method, body));
  if (!res.ok) {
    const reason = await refusalReason(res, reasonStatuses);
    throw new ApiError(`${method} ${path} failed: ${res.status}`, res.status, reason);
  }
  return res.json();
}

// A refused rule says why in a short plain-text body: the dialog shows a 400's as it is, and tells
// the two 409s (an overlap, the rule cap) apart by theirs.
function sendClockRule(method: "POST" | "PUT", path: string, input: CameraClockRuleInput): Promise<CameraClockRule> {
  return sendWithReason<CameraClockRule>(method, path, input, [400, 409]);
}

function createCameraClockRule(input: CameraClockRuleInput): Promise<CameraClockRule> {
  return sendClockRule("POST", "/camera-clock-rules", input);
}

// A full replacement of the rule.
function updateCameraClockRule(id: string, input: CameraClockRuleInput): Promise<CameraClockRule> {
  return sendClockRule("PUT", `/camera-clock-rules/${id}`, input);
}

function deleteCameraClockRule(id: string): Promise<void> {
  return del(`/camera-clock-rules/${id}`);
}

// The cameras in the caller's own library, most items first, for the clock rule dialog.
function listCameras(): Promise<LibraryCamera[]> {
  return getList<LibraryCamera>("/cameras");
}

function getSettings(): Promise<UserSettings> {
  return get<UserSettings>("/me/settings");
}

// A refused setting says why in a short plain-text body (an unknown zone), kept in ApiError.reason.
function updateSettings(change: UserSettingsPatch): Promise<UserSettings> {
  return sendWithReason<UserSettings>("PATCH", "/me/settings", change);
}

// Sets an own item's capture time to a wall clock ("YYYY-MM-DDTHH:MM:SS") read in an IANA zone or
// at a fixed UTC offset. A refused body (a wall clock the zone skipped, an unknown zone) says why
// in ApiError.reason; a foreign, shared or deleted item is a 404.
function setTakenAt(id: string, wall: string, zone: TakenAtZone): Promise<SetTakenAtResult> {
  const body = "zone" in zone ? { wall, zone: zone.zone } : { wall, utc_offset_min: zone.utcOffsetMin };
  return sendWithReason<SetTakenAtResult>("PATCH", `/media/${id}/taken-at`, body);
}

// Moves the capture times of own items (at most 5000) by the same number of seconds, later when
// positive. One item that is not the caller's own makes the whole request a 404, and nothing moves.
function shiftTimes(mediaIds: string[], deltaSeconds: number): Promise<ShiftTimesResult> {
  return sendWithReason<ShiftTimesResult>("POST", "/media/shift-time", {
    media_ids: mediaIds,
    delta_seconds: deltaSeconds,
  });
}

// Re-matches the caller's whole library.
async function runMatcher(): Promise<void> {
  const res = await request("/gps-matcher/run", { method: "POST" });
  if (!res.ok) throw new ApiError(`POST /gps-matcher/run failed: ${res.status}`, res.status);
}

// The checks run on the server for seconds, so the Checks view cancels a request it no longer
// needs: when it closes or loads again.
async function getCancellable<T>(path: string, signal?: AbortSignal, params?: QueryParams): Promise<T> {
  const res = await request(withQuery(path, params), { signal });
  if (!res.ok) throw new ApiError(`GET ${path} failed: ${res.status}`, res.status);
  return res.json();
}

// The library-wide checks of the caller's tracks and items. A 504 is the server's time limit.
function checks(signal?: AbortSignal): Promise<ChecksResponse> {
  return getCancellable<ChecksResponse>("/checks", signal);
}

// Each log file against the other formats, and its time spacing, from the log file at index `from`
// on, as far as the server's time budget goes (up to a minute). The index is a number, never a
// label, so no file name reaches the URL. A 503 means the server is running other comparisons.
function checksAgreement(from: number, signal?: AbortSignal): Promise<AgreementResponse> {
  return getCancellable<AgreementResponse>("/checks/agreement", signal, { from: String(from) });
}

function listChallenges(): Promise<ChallengeSummary[]> {
  return getList<ChallengeSummary>("/challenges");
}

// Makes a challenge of five located items drawn from what `filters` select (filterParams, as for a
// free round) and every player can read. The caller always plays; memberIds are the others. A
// refusal says why in ApiError.reason: a 400's body, or a 409's when fewer than five items match.
function createChallenge(name: string, memberIds: string[], filters: QueryParams): Promise<ChallengeSummary> {
  const path = withQuery("/challenges", filters);
  return sendWithReason<ChallengeSummary>("POST", path, { name, member_ids: memberIds }, [400, 409]);
}

// One challenge as the caller stands in it: their next round, or every round's results once they
// have none left. A 404 means it is gone or the caller does not play in it.
function getChallenge(id: string, signal?: AbortSignal): Promise<Challenge> {
  return getCancellable<Challenge>(`/challenges/${id}`, signal);
}

// The caller's one guess for a round; `date` is YYYY-MM-DD or "". A 409 means the round was
// guessed before, a 404 that it is gone or its item is no longer shared with the caller.
function guessChallenge(
  id: string,
  position: number,
  guess: { lat: number; lng: number },
  date: string
): Promise<GuessResult> {
  return post<GuessResult>(`/challenges/${id}/guess`, {
    position,
    guess_lat: guess.lat,
    guess_lng: guess.lng,
    guess_date: date,
  });
}

function deleteChallenge(id: string): Promise<void> {
  return del(`/challenges/${id}`);
}

// Takes the caller, and their guesses, out of a challenge someone else owns.
async function leaveChallenge(id: string): Promise<void> {
  const path = `/challenges/${id}/leave`;
  const res = await request(path, { method: "POST" });
  if (!res.ok) throw new ApiError(`POST ${path} failed: ${res.status}`, res.status);
}

export const api = {
  get,
  post,
  patch,
  delete: del,
  listMedia,
  getMediaUrl,
  getKeyframes,
  updateViewSettings,
  replaceKeyframes,
  updateLocation,
  me,
  logout,
  upload,
  knownHashes,
  uploadGpsTracks,
  listTokens,
  createToken,
  revokeToken,
  adminListUsers,
  adminSetDisabled,
  adminCreateInvite,
  listAlbums,
  createAlbum,
  renameAlbum,
  deleteAlbum,
  listAlbumMembers,
  addAlbumMember,
  removeAlbumMember,
  addToAlbum,
  removeFromAlbum,
  mediaAlbums,
  listUsers,
  mediaProvenance,
  mediaProvenanceFile,
  runMatcher,
  checks,
  checksAgreement,
  listCameraClockRules,
  createCameraClockRule,
  updateCameraClockRule,
  deleteCameraClockRule,
  listCameras,
  getSettings,
  updateSettings,
  setTakenAt,
  shiftTimes,
  listChallenges,
  createChallenge,
  getChallenge,
  guessChallenge,
  deleteChallenge,
  leaveChallenge,
};
