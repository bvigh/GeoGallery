import type {
  AdminUser,
  Album,
  AlbumMember,
  ApiToken,
  CreatedApiToken,
  CurrentUser,
  DirectoryUser,
  GpsImportResult,
  OrientationKeyframe,
} from "@/types";

// What to add to or remove from an album: explicit media, or a trip's current media.
export type AlbumMediaSelection = { mediaIds: string[] } | { tripId: string };

const BASE = "/api";

export class ApiError extends Error {
  constructor(message: string, public status: number) {
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

async function get<T>(path: string, params?: Record<string, string | string[]>): Promise<T> {
  let url = path;
  if (params) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (Array.isArray(v)) {
        v.forEach((s) => qs.append(k, s));
      } else if (v !== "") {
        qs.set(k, v);
      }
    }
    const s = qs.toString();
    if (s) url += "?" + s;
  }
  const res = await request(url);
  if (!res.ok) throw new ApiError(`GET ${path} failed: ${res.status}`, res.status);
  return res.json();
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
async function upload(form: FormData, albumId?: string): Promise<{
  uploaded: string[] | null;
  skipped?: string[] | null;
  failed?: string[] | null;
  album_added?: number;
}> {
  const path = albumId ? `/media/upload?album_id=${encodeURIComponent(albumId)}` : "/media/upload";
  const res = await request(path, { method: "POST", body: form });
  if (!res.ok) throw new ApiError(`POST /media/upload failed: ${res.status}`, res.status);
  return res.json();
}

async function importGpsTracks(file: File): Promise<GpsImportResult> {
  const res = await request("/gps-tracks/import", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: file,
  });
  if (!res.ok) throw new ApiError(`POST /gps-tracks/import failed: ${res.status}`, res.status);
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

export const api = {
  get,
  post,
  patch,
  delete: del,
  getMediaUrl,
  getKeyframes,
  updateViewSettings,
  replaceKeyframes,
  updateLocation,
  me,
  logout,
  upload,
  importGpsTracks,
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
};
