import { useState, useRef, useEffect } from "react";
import { MapContainer, TileLayer, Marker, useMapEvents, useMap } from "react-leaflet";
import L from "leaflet";
import type { MediaItem } from "../types";
import { api } from "../api/client";
import "leaflet/dist/leaflet.css";

function MapClickHandler({ onMapClick }: { onMapClick: (lat: number, lng: number) => void }) {
  useMapEvents({
    click: (e) => onMapClick(e.latlng.lat, e.latlng.lng),
  });
  return null;
}

function MapFlyTo({ target }: { target: { lat: number; lng: number; seq: number } | null }) {
  const map = useMap();
  const lastSeq = useRef(-1);
  useEffect(() => {
    if (target && target.seq !== lastSeq.current) {
      lastSeq.current = target.seq;
      map.flyTo([target.lat, target.lng], Math.max(map.getZoom(), 15), { duration: 1 });
    }
  }, [map, target]);
  return null;
}

const pickedMarkerIcon = L.divIcon({
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#22c55e;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  className: "",
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

function parseCoords(input: string): { lat: number; lng: number } | null {
  const match = input.trim().match(/^(-?\d+\.?\d*)[,\s]+(-?\d+\.?\d*)$/);
  if (!match) return null;
  const lat = parseFloat(match[1]);
  const lng = parseFloat(match[2]);
  if (isNaN(lat) || isNaN(lng) || lat < -90 || lat > 90 || lng < -180 || lng > 180) return null;
  return { lat, lng };
}

interface Props {
  item: MediaItem;
  onClose: () => void;
  onSaved: (lat: number, lng: number) => void;
}

export function EditLocationModal({ item, onClose, onSaved }: Props) {
  const [pickedLat, setPickedLat] = useState<number | null>(item.lat);
  const [pickedLng, setPickedLng] = useState<number | null>(item.lng);
  const [saving, setSaving] = useState(false);
  const [pasteInput, setPasteInput] = useState("");
  const [pasteError, setPasteError] = useState(false);
  const [flyTarget, setFlyTarget] = useState<{ lat: number; lng: number; seq: number } | null>(null);

  const initialCenter: [number, number] =
    item.lat != null && item.lng != null ? [item.lat, item.lng] : [20, 0];
  const initialZoom = item.lat != null ? 10 : 2;

  async function handleSave() {
    if (pickedLat == null || pickedLng == null) return;
    setSaving(true);
    try {
      await api.updateLocation(item.id, pickedLat, pickedLng);
      onSaved(pickedLat, pickedLng);
    } finally {
      setSaving(false);
    }
  }

  function handlePasteGo() {
    const coords = parseCoords(pasteInput);
    if (!coords) {
      setPasteError(true);
      setTimeout(() => setPasteError(false), 1200);
      return;
    }
    setPickedLat(coords.lat);
    setPickedLng(coords.lng);
    setFlyTarget({ lat: coords.lat, lng: coords.lng, seq: (flyTarget?.seq ?? 0) + 1 });
    setPasteInput("");
    setPasteError(false);
  }

  return (
    <div className="fixed inset-0 z-[1200] flex flex-col bg-black">
      <div className="flex items-center justify-between px-4 py-2 bg-black/80 text-white text-sm shrink-0">
        <span className="font-medium">Edit GPS location — {item.filename}</span>
        <button onClick={onClose} className="text-white hover:text-gray-300 text-xl leading-none">
          ✕
        </button>
      </div>

      <div className="flex-1 relative">
        <MapContainer
          center={initialCenter}
          zoom={initialZoom}
          className="h-full w-full"
          zoomControl
        >
          <TileLayer
            attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'
            url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
          />
          <MapClickHandler
            onMapClick={(lat, lng) => {
              setPickedLat(lat);
              setPickedLng(lng);
            }}
          />
          <MapFlyTo target={flyTarget} />
          {pickedLat != null && pickedLng != null && (
            <Marker
              position={[pickedLat, pickedLng]}
              icon={pickedMarkerIcon}
              draggable
              eventHandlers={{
                dragend: (e) => {
                  const pos = (e.target as L.Marker).getLatLng();
                  setPickedLat(pos.lat);
                  setPickedLng(pos.lng);
                },
              }}
            />
          )}
        </MapContainer>
      </div>

      <div className="bg-black/80 text-white px-4 py-3 flex flex-col gap-2 shrink-0">
        <div className="flex gap-2">
          <input
            type="text"
            placeholder="Paste coords from Google Maps (e.g. 48.8566, 2.3522)"
            value={pasteInput}
            onChange={(e) => { setPasteInput(e.target.value); setPasteError(false); }}
            onKeyDown={(e) => e.key === "Enter" && handlePasteGo()}
            className={`flex-1 rounded px-3 py-1.5 text-sm placeholder:text-white/40 outline-none bg-white/10 focus:bg-white/15 transition-colors ${pasteError ? "ring-1 ring-red-400 bg-red-900/20" : ""}`}
          />
          <button
            onClick={handlePasteGo}
            disabled={!pasteInput.trim()}
            className="rounded bg-white/20 px-3 py-1.5 text-sm font-medium hover:bg-white/30 disabled:opacity-40"
          >
            Go
          </button>
        </div>
        <div className="flex items-center justify-between">
          <span className="text-sm text-white/60">
            {pickedLat != null && pickedLng != null
              ? `${pickedLat.toFixed(6)}, ${pickedLng.toFixed(6)}`
              : "Click on the map to set location"}
          </span>
          <button
            onClick={handleSave}
            disabled={pickedLat == null || saving}
            className="rounded bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
          >
            {saving ? "Saving…" : "Save location"}
          </button>
        </div>
      </div>
    </div>
  );
}
