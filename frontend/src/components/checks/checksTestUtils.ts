// Synthetic checks responses for the Checks view's tests. Nothing here is real data.

import type { Comparison } from "@/lib/checks";
import type {
  AgreementFile,
  AgreementResponse,
  CheckCode,
  ChecksCheck,
  ChecksFile,
  ChecksGroup,
  ChecksResponse,
} from "@/types";

export function makeFile(overrides: Partial<ChecksFile> = {}): ChecksFile {
  return {
    source: "gpx:walk-a.gpx",
    format: "gpx",
    kind: "recorded",
    points: 100,
    first: "2000-01-01T00:00:00Z",
    last: "2000-01-01T01:00:00Z",
    items_placed: 0,
    ...overrides,
  };
}

export function makeGroup(overrides: Partial<ChecksGroup> = {}): ChecksGroup {
  return { key: "gpx+nmea", label: "gpx+nmea", count: 1, evidence: {}, media_ids: [], ...overrides };
}

// The seven checks in the server's order.
const CHECKS: [CheckCode, ChecksCheck["severity"]][] = [
  ["pair_impossible", "high"],
  ["future_track_points", "high"],
  ["taken_after_upload", "high"],
  ["stale_estimates", "high"],
  ["pair_far", "low"],
  ["orphan_points", "low"],
  ["time_may_be_estimated_used", "low"],
];

// All seven checks, with nothing found except the groups given.
export function makeCheckList(found: Partial<Record<CheckCode, ChecksGroup[]>> = {}): ChecksCheck[] {
  return CHECKS.map(([code, severity]) => {
    const groups = found[code] ?? [];
    return { code, severity, total: groups.reduce((sum, group) => sum + group.count, 0), groups };
  });
}

export function makeChecks(overrides: Partial<ChecksResponse> = {}): ChecksResponse {
  return {
    generated_at: "2000-01-02T03:04:05Z",
    window_s: 3600,
    files: [makeFile()],
    residuals: [],
    checks: makeCheckList(),
    ...overrides,
  };
}

export function makeAgreementFile(overrides: Partial<AgreementFile> = {}): AgreementFile {
  return { source: "gpx:walk-a.gpx", samples: 20, matched: 18, median_m: 12, modal_gap_share: 0.94, ...overrides };
}

// One answer of a comparison: the last one, covering its files alone, unless overridden.
export function makeAgreement(overrides: Partial<AgreementResponse> = {}): AgreementResponse {
  const files = overrides.files ?? [makeAgreementFile()];
  const nextFrom = overrides.next_from ?? null;
  return {
    generated_at: "2000-01-02T03:05:00Z",
    files,
    truncated: nextFrom !== null,
    total_files: files.length,
    next_from: nextFrom,
    ...overrides,
  };
}

// A comparison as the view keeps it, from the answers it got.
export function makeComparison(files: AgreementFile[], total = files.length, next: number | null = null): Comparison {
  return { files: new Map(files.map((file) => [file.source, file])), total, next };
}
