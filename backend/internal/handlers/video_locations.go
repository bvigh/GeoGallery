package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"codeberg.org/bvigh/geogallery/internal/geo"
	"codeberg.org/bvigh/geogallery/internal/ingest"
	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// videoProbeTimeout bounds one ffprobe run over the network.
const videoProbeTimeout = 2 * time.Minute

// denseLogConfidence marks an interpolation between track points a few seconds apart: at the
// default one-hour match window, 0.999 means at most 3.6 s. That is a GPS log recording during the
// clip, and it beats the tag, which can be the phone's last known fix. On a real library, 16 of 67
// tagged videos had such a log: the interpolation lay on it, and the tag was up to 4 km off it.
const denseLogConfidence = 0.999

// VideoLocationDeps holds what RunVideoLocations needs; the operator `server video-locations`
// command builds it.
type VideoLocationDeps struct {
	pool *pgxpool.Pool
	// probe reads the container metadata of a stored object.
	probe func(ctx context.Context, storageKey string) (ingest.VideoProbeResult, error)
}

func NewVideoLocationDeps(pool *pgxpool.Pool, s *storage.Client) VideoLocationDeps {
	return VideoLocationDeps{pool: pool, probe: func(ctx context.Context, key string) (ingest.VideoProbeResult, error) {
		url, err := s.PresignGet(ctx, key)
		if err != nil {
			return ingest.VideoProbeResult{}, err
		}
		return ingest.ProbeURL(ctx, url)
	}}
}

// VideoLocationChange is one video RunVideoLocations places (in a dry run: would place).
type VideoLocationChange struct {
	MediaID   string
	Filename  string
	Lat, Lng  float64
	OldSource string   // gps_source before the change; "" without a location
	MovedM    *float64 // metres from the old location; nil without one
}

// VideoLocationResult counts what RunVideoLocations found among the videos it may change.
type VideoLocationResult struct {
	Checked  int
	Located  int // the location moved to the file's own tag (in a dry run: would move)
	DenseLog int // interpolated from a GPS log recording during the clip: left alone, not probed
	NoTag    int // the file has no usable location tag: left alone
	Failed   int // object unreadable or the update failed: logged
}

// RunVideoLocations gives ownerID's videos the position in their own container tag, which ingest
// did not read when they were stored. Only videos without a location or with an interpolated one
// change, so an EXIF or manual location is never overwritten, and a re-run finds nothing new. An
// interpolation from a dense GPS log stays too (denseLogConfidence).
//
// Without apply nothing is written. Unlike a retime this creates no track points, as registerMedia
// creates none for a video's tag, so no other item's location depends on the result and neither the
// matcher nor trip detection has to run afterwards. report is called for each change. On
// cancellation the loop stops after the current item and ctx.Err() is returned.
//
// GPS stays per owner: every query here filters on ownerID literally, like RunGPSMatcher.
func RunVideoLocations(ctx context.Context, deps VideoLocationDeps, ownerID string, apply bool, report func(VideoLocationChange)) (VideoLocationResult, error) {
	slog.Info("video-locations: started", "owner_id", ownerID, "apply", apply)
	var res VideoLocationResult

	items, err := videoLocationCandidates(ctx, deps.pool, ownerID)
	if err != nil {
		return res, fmt.Errorf("list media: %w", err)
	}

	for _, m := range items {
		if err := ctx.Err(); err != nil {
			break
		}
		res.Checked++
		if m.confidence != nil && *m.confidence >= denseLogConfidence {
			res.DenseLog++
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
		probe, err := deps.probe(probeCtx, m.storageKey)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Error("video-locations: probe failed", "id", m.id, "key", m.storageKey, "err", err)
			res.Failed++
			continue
		}
		if probe.Lat == nil {
			res.NoTag++
			continue
		}

		change := VideoLocationChange{
			MediaID: m.id, Filename: m.filename, Lat: *probe.Lat, Lng: *probe.Lng, OldSource: m.gpsSource,
		}
		if m.lat != nil && m.lng != nil {
			moved := geo.HaversineKm(*m.lat, *m.lng, change.Lat, change.Lng) * 1000
			change.MovedM = &moved
		}
		if apply {
			if err := applyVideoLocation(ctx, deps.pool, ownerID, change); err != nil {
				if ctx.Err() != nil {
					break
				}
				slog.Error("video-locations: update failed", "id", m.id, "err", err)
				res.Failed++
				continue
			}
		}
		res.Located++
		report(change)
	}
	if err := ctx.Err(); err != nil {
		slog.Warn("video-locations: interrupted", "checked", res.Checked, "located", res.Located)
		return res, err
	}
	slog.Info("video-locations: complete", "apply", apply, "checked", res.Checked, "located", res.Located, "failed", res.Failed)
	return res, nil
}

type videoLocationCandidate struct {
	id, filename, storageKey, gpsSource string
	lat, lng, confidence                *float64
}

// videoLocationCandidates lists the videos whose location a tag may replace, read up front so no
// connection is held while objects are probed.
func videoLocationCandidates(ctx context.Context, pool *pgxpool.Pool, ownerID string) ([]videoLocationCandidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, filename, storage_key, coalesce(gps_source, ''),
		       ST_Y(location::geometry), ST_X(location::geometry), gps_confidence
		FROM media
		WHERE owner_id = $1 AND media_type IN ('video', 'video_360')
		  AND (gps_source IS NULL OR gps_source = 'interpolated')
		ORDER BY taken_at NULLS LAST, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []videoLocationCandidate
	for rows.Next() {
		var c videoLocationCandidate
		if err := rows.Scan(&c.id, &c.filename, &c.storageKey, &c.gpsSource, &c.lat, &c.lng, &c.confidence); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// applyVideoLocation repeats the candidate condition, so a location set by hand since the list was
// read is not overwritten.
func applyVideoLocation(ctx context.Context, pool *pgxpool.Pool, ownerID string, c VideoLocationChange) error {
	tag, err := pool.Exec(ctx, `
		UPDATE media
		SET location       = ST_SetSRID(ST_MakePoint($3, $4), 4326),
		    gps_confidence = NULL,
		    gps_source     = 'exif'
		WHERE id = $1 AND owner_id = $2
		  AND (gps_source IS NULL OR gps_source = 'interpolated')`,
		c.MediaID, ownerID, c.Lng, c.Lat)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("the row changed since it was read; re-run to pick it up")
	}
	return nil
}
