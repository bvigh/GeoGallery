import type {
  AdminUser,
  ApiToken,
  CreatedApiToken,
  CurrentUser,
  GpsImportResult,
  OrientationKeyframe,
} from "@/types";

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

async function upload(form: FormData): Promise<{ uploaded: string[] | null; failed?: string[] | null }> {
  const res = await request("/media/upload", { method: "POST", body: form });
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
};
