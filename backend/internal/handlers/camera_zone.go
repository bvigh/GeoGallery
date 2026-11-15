package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
)

// rowQuerier is what loadCameraZoneSetting needs: a pool, a connection or a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadCameraZoneSetting returns ownerID's camera zone setting (users.camera_timezone), nil when it
// is unset. A stored name this server cannot load (a zone gone from its tzdata) is an error, so no
// file of the owner's is read in a zone they did not choose.
func loadCameraZoneSetting(ctx context.Context, db rowQuerier, ownerID string) (*time.Location, error) {
	var name *string
	if err := db.QueryRow(ctx, `SELECT camera_timezone FROM users WHERE id = $1`, ownerID).Scan(&name); err != nil {
		return nil, fmt.Errorf("load camera zone setting: %w", err)
	}
	return loadZoneSetting(name)
}

// loadZoneSetting loads a stored camera zone setting; nil stays nil.
func loadZoneSetting(name *string) (*time.Location, error) {
	if name == nil {
		return nil, nil
	}
	loc, err := ingest.LoadZone(*name)
	if err != nil {
		return nil, fmt.Errorf("load camera zone setting: %w", err)
	}
	return loc, nil
}

// withZoneSetting returns server read in setting, an owner's stored camera zone setting, when it is
// not nil.
func withZoneSetting(server ingest.CameraZone, setting *string) (ingest.CameraZone, error) {
	loc, err := loadZoneSetting(setting)
	if err != nil {
		return ingest.CameraZone{}, err
	}
	return server.In(loc, ingest.CameraZoneFromUser), nil
}

// ownerCameraZone returns server, the server's camera zone and rules, read in ownerID's camera zone
// setting when they have one.
func ownerCameraZone(ctx context.Context, db rowQuerier, server ingest.CameraZone, ownerID string) (ingest.CameraZone, error) {
	loc, err := loadCameraZoneSetting(ctx, db, ownerID)
	if err != nil {
		return ingest.CameraZone{}, err
	}
	return server.In(loc, ingest.CameraZoneFromUser), nil
}

// ingestCameraZone is the zone an upload or a scan reads ownerID's files in: batch, the zone it
// named, when given; else the owner's setting; else server. The owner's setting is not read when a
// batch zone wins.
func ingestCameraZone(ctx context.Context, db rowQuerier, server ingest.CameraZone, ownerID string, batch *time.Location) (ingest.CameraZone, error) {
	if batch != nil {
		return server.In(batch, ingest.CameraZoneFromBatch), nil
	}
	return ownerCameraZone(ctx, db, server, ownerID)
}

// storedItemCameraZone is the zone a stored item is read in again (server retime, the file re-read):
// the zone its upload or scan named, which stays with its files, when its capture_evidence records one
// (camera_zone_from "batch", camera_zone the name); otherwise owner, the zone the owner's uploads get
// today. recordedFrom and recordedName are those two keys, "" when absent.
func storedItemCameraZone(owner ingest.CameraZone, recordedFrom, recordedName string) (ingest.CameraZone, error) {
	if recordedFrom != ingest.CameraZoneFromBatch {
		return owner, nil
	}
	loc, err := ingest.LoadZone(recordedName)
	if err != nil {
		return ingest.CameraZone{}, fmt.Errorf("the zone named for its upload: %w", err)
	}
	return owner.In(loc, ingest.CameraZoneFromBatch), nil
}
