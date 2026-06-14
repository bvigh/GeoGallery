import type { OrientationKeyframe } from "../types";

const BASE = "/api";

async function get<T>(path: string, params?: Record<string, string | string[]>): Promise<T> {
  let url = `${BASE}${path}`;
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
  const res = await fetch(url);
  if (!res.ok) throw new Error(`GET ${path} failed: ${res.status}`);
  return res.json();
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${path} failed: ${res.status}`);
  return res.json();
}

async function patch<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${path} failed: ${res.status}`);
  return res.json();
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
  await fetch(`${BASE}/media/${id}/view-settings`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
}

async function replaceKeyframes(id: string, keyframes: OrientationKeyframe[]): Promise<void> {
  await fetch(`${BASE}/media/${id}/keyframes`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(keyframes),
  });
}

async function del(path: string): Promise<void> {
  const res = await fetch(`${BASE}${path}`, { method: "DELETE" });
  if (!res.ok) throw new Error(`DELETE ${path} failed: ${res.status}`);
}

export const api = { get, post, patch, delete: del, getMediaUrl, getKeyframes, updateViewSettings, replaceKeyframes };
