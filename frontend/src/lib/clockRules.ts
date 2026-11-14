// How camera clock rules and library cameras are named, on the Settings page and in the evidence
// view alike, so a rule reads the same in both places.

import { formatOffset } from "@/lib/time";
import type { CameraClockRuleInput, LibraryCamera } from "@/types";

// The backend refuses a 101st rule.
export const MAX_CLOCK_RULES = 100;

// Every offset a rule can give: UTC-12:00 to UTC+14:00 in 15-minute steps.
export const CLOCK_OFFSETS: number[] = Array.from({ length: (840 + 720) / 15 + 1 }, (_, i) => -720 + i * 15);

export function formatUtcOffset(minutes: number): string {
  return `UTC${formatOffset(minutes)}`;
}

// Make and model as one name. Many cameras repeat the make in the model ("Canon EOS R5"), and
// that is not written twice.
export function cameraName(make: string, model: string): string {
  return model.toLowerCase().startsWith(make.toLowerCase()) ? model : `${make} ${model}`;
}

function fold(value: string): string {
  return value.trim().toLowerCase();
}

// Rules match make and model without case or surrounding space, as ingest does.
export function sameCamera(a: { make: string; model: string }, b: { make: string; model: string }): boolean {
  return fold(a.make) === fold(b.make) && fold(a.model) === fold(b.model);
}

// A rule's days, on the camera's own clock. They are calendar days, not instants, so they are
// shown as written and never converted to a zone.
export function rulePeriod({ starts_on, ends_on }: Pick<CameraClockRuleInput, "starts_on" | "ends_on">): string {
  if (starts_on && ends_on) return starts_on === ends_on ? `on ${starts_on}` : `${starts_on} – ${ends_on}`;
  if (starts_on) return `from ${starts_on}`;
  if (ends_on) return `until ${ends_on}`;
  return "always";
}

function utcYear(iso: string | null): number | null {
  if (iso === null) return null;
  const year = new Date(iso).getUTCFullYear();
  return Number.isNaN(year) ? null : year;
}

// "2019–2022", "2019", or null when the camera has no dated item.
export function cameraYears(camera: Pick<LibraryCamera, "first_taken_at" | "last_taken_at">): string | null {
  const first = utcYear(camera.first_taken_at);
  const last = utcYear(camera.last_taken_at);
  if (first === null && last === null) return null;
  if (first === null || last === null || first === last) return String(first ?? last);
  return `${first}–${last}`;
}

// "Panasonic DC-TZ200 · 595 items · 2019–2022"
export function libraryCameraLabel(camera: LibraryCamera): string {
  const parts = [cameraName(camera.make, camera.model), `${camera.items} ${camera.items === 1 ? "item" : "items"}`];
  const years = cameraYears(camera);
  if (years) parts.push(years);
  return parts.join(" · ");
}
