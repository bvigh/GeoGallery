import { useState } from "react";
import { MapContainer, TileLayer, Marker, useMapEvents } from "react-leaflet";
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

const pickedMarkerIcon = L.divIcon({
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#22c55e;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  className: "",
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

interface Props {
  item: MediaItem;
  onClose: () => void;
  onSaved: (lat: number, lng: number) => void;
}

export function EditLocationModal({ item, onClose, onSaved }: Props) {
  const [pickedLat, setPickedLat] = useState<number | null>(item.lat);
  const [pickedLng, setPickedLng] = useState<number | null>(item.lng);
  const [saving, setSaving] = useState(false);

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

      <div className="bg-black/80 text-white px-4 py-3 flex items-center justify-between shrink-0">
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
  );
}
