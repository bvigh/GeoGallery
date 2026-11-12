// Synthetic provenance responses for the evidence view's tests. Nothing here is real data.

import type {
  Bracket,
  CaptureEvidence,
  Estimate,
  FileReread,
  OtherFormat,
  OtherPoint,
  ProvenanceResponse,
  TrackResponse,
} from "@/types";

export const SERVER_ZONE = { name: "Etc/GMT-1", set: true, utc_fallback: false, offset_now_min: 60 };

export function makeBracket(overrides: Partial<Bracket> = {}): Bracket {
  return {
    source: "gpx:walk-a.gpx",
    format: "gpx",
    kind: "log",
    t: "2000-01-01T00:00:00Z",
    lat: 10,
    lng: 20,
    accuracy_m: 5,
    altitude_m: 100,
    gap_s: 60,
    origin: null,
    ...overrides,
  };
}

export function makeOtherPoint(overrides: Partial<OtherPoint> = {}): OtherPoint {
  return {
    source: "nmea:a.nmea",
    t: "2000-01-01T00:00:30Z",
    lat: 10.3,
    lng: 20.3,
    gap_s: 30,
    accuracy_m: null,
    distance_m: 100,
    used: false,
    self: false,
    ...overrides,
  };
}

// A format with a point on each side of the default item's time (00:01:00), and its own estimate.
export function makeOtherFormat(overrides: Partial<OtherFormat> = {}): OtherFormat {
  return {
    format: "nmea",
    before: makeOtherPoint({ t: "2000-01-01T00:00:30Z", lat: 10.3, lng: 20.3, gap_s: 30 }),
    after: makeOtherPoint({ t: "2000-01-01T00:02:00Z", lat: 10.4, lng: 20.4, gap_s: 60 }),
    alone: { lat: 10.33, lng: 20.33, shape: "two_sided", confidence: 0.9, distance_m: 1500 },
    used: false,
    ...overrides,
  };
}

export function makeEstimate(
  overrides: Partial<Estimate> = {},
  arithmetic: Partial<Estimate["arithmetic"]> = {}
): Estimate {
  return {
    lat: 10.1,
    lng: 20.1,
    confidence: 0.5,
    delta_m: 0,
    before: makeBracket({ t: "2000-01-01T00:00:00Z", lat: 10, lng: 20, gap_s: 60 }),
    after: makeBracket({ t: "2000-01-01T00:03:00Z", lat: 10.2, lng: 20.2, gap_s: 120 }),
    ...overrides,
    arithmetic: {
      shape: "two_sided",
      pair_gap_s: 180,
      pair_km: 2,
      t: 0.33,
      time_factor: 0.9,
      distance_factor: 0.8,
      confidence: 0.5,
      implied_kmh: 40,
      ...arithmetic,
    },
  };
}

export function makeTrack(overrides: Partial<TrackResponse> = {}): TrackResponse {
  return {
    windows: [],
    match_window_s: 3600,
    total_points: 0,
    returned_points: 0,
    bucket_s: 0,
    sources: [],
    points: [],
    ...overrides,
  };
}

export function makeEvidence(overrides: Partial<CaptureEvidence> = {}): CaptureEvidence {
  return {
    v: 1,
    by: "ingest",
    src: "exif_original",
    zone: "camera",
    wall: "2000:01:01 01:01:00",
    gps_utc: null,
    wall_minus_gps_s: null,
    offset_tag: null,
    offset_ignored: false,
    offset_used_min: 60,
    camera_zone: "Etc/GMT-1",
    camera_zone_utc_fallback: false,
    make: null,
    model: null,
    exif_partial: false,
    creation_time: null,
    file_mtime: null,
    duration_s: null,
    encoder: null,
    codec: null,
    transcoded: false,
    ...overrides,
  };
}

export function makeReread(overrides: Partial<FileReread> = {}): FileReread {
  return {
    basis: "reread_now",
    stored_taken_at: "2000-01-01T00:01:00Z",
    uploaded_at: "2000-01-02T00:00:00Z",
    file: {
      wall_clock: "2000:01:01 01:01:00",
      wall_tag: "DateTimeOriginal",
      offset_tag: null,
      offset_ignored: false,
      gps_stamp: null,
      wall_minus_gps_s: null,
      make: null,
      model: null,
      video: null,
    },
    rule_today: {
      source: "exif_original",
      zone_rule: "camera",
      offset_min: 60,
      zone_name: "Etc/GMT-1",
      taken_at: "2000-01-01T00:01:00Z",
    },
    delta_s: 0,
    class: "same",
    checks: [],
    tags: [],
    ...overrides,
  };
}

export type ResponseOverrides = {
  item?: Partial<ProvenanceResponse["item"]>;
  time?: Partial<ProvenanceResponse["time"]>;
  placement?: Partial<ProvenanceResponse["placement"]>;
  context?: Partial<TrackResponse>;
};

export function makeResponse(id: string, overrides: ResponseOverrides = {}): ProvenanceResponse {
  return {
    item: {
      id,
      filename: `${id}.jpg`,
      media_type: "photo",
      taken_at: "2000-01-01T00:01:00Z",
      created_at: "2000-01-02T00:00:00Z",
      lat: 10.1,
      lng: 20.1,
      gps_source: "interpolated",
      gps_confidence: 0.5,
      camera_model: null,
      width_px: null,
      height_px: null,
      duration_sec: null,
      ...overrides.item,
    },
    time: {
      recorded: "ingest",
      evidence: makeEvidence(),
      server_camera_zone: SERVER_ZONE,
      ...overrides.time,
    },
    placement: {
      state: "current",
      window_s: 3600,
      now: makeEstimate(),
      independent: null,
      nearest_outside: null,
      others: [],
      warnings: [],
      ...overrides.placement,
    },
    context: makeTrack(overrides.context),
  };
}
