import { MapContainer, TileLayer, Marker, Popup } from "react-leaflet";
import MarkerClusterGroup from "@changey/react-leaflet-markercluster";
import type { MediaItem } from "../../types";
import "leaflet/dist/leaflet.css";
import "@changey/react-leaflet-markercluster/dist/styles.min.css";
import L from "leaflet";

function markerIcon(item: MediaItem) {
  const color =
    item.gps_confidence !== null && item.gps_confidence < 0.5 ? "#f97316" : "#3b82f6";
  const is360 = item.media_type === "photo_360" || item.media_type === "video_360";
  const size = is360 ? 16 : 12;
  const half = size / 2;
  const outline = is360 ? "outline:2px solid #a855f7;outline-offset:2px;" : "";
  return L.divIcon({
    html: `<div style="width:${size}px;height:${size}px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 1px 3px rgba(0,0,0,.4);${outline}"></div>`,
    className: "",
    iconSize: [size, size],
    iconAnchor: [half, half],
  });
}

function createClusterIcon(cluster: { getChildCount(): number }) {
  const count = cluster.getChildCount();
  return L.divIcon({
    html: `<div style="width:36px;height:36px;border-radius:50%;background:#3b82f6;border:2px solid white;box-shadow:0 1px 4px rgba(0,0,0,.4);display:flex;align-items:center;justify-content:center;color:white;font-size:13px;font-weight:600;">${count}</div>`,
    className: "",
    iconSize: [36, 36],
    iconAnchor: [18, 18],
  });
}

interface Props {
  items: MediaItem[];
  onSelect?: (item: MediaItem) => void;
}

export function MediaMap({ items, onSelect }: Props) {
  const withLocation = (items ?? []).filter((m) => m.lat !== null && m.lng !== null);

  return (
    <MapContainer
      center={[20, 0]}
      zoom={2}
      className="h-full w-full"
    >
      <TileLayer
        attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'
        url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
      />
      <MarkerClusterGroup chunkedLoading iconCreateFunction={createClusterIcon}>
        {withLocation.map((item) => (
          <Marker
            key={item.id}
            position={[item.lat!, item.lng!]}
            icon={markerIcon(item)}
            eventHandlers={{ click: () => onSelect?.(item) }}
          >
            <Popup>
              <div className="text-sm">
                <p className="font-medium">{item.filename}</p>
                {item.taken_at && (
                  <p className="text-muted-foreground">
                    {new Date(item.taken_at).toLocaleDateString()}
                  </p>
                )}
              </div>
            </Popup>
          </Marker>
        ))}
      </MarkerClusterGroup>
    </MapContainer>
  );
}
