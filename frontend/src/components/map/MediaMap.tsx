import { MapContainer, TileLayer, Marker, Popup } from "react-leaflet";
import type { MediaItem } from "../../types";
import "leaflet/dist/leaflet.css";
import L from "leaflet";

function markerIcon(confidence: number | null) {
  const color = confidence !== null && confidence < 0.5 ? "#f97316" : "#3b82f6";
  return L.divIcon({
    html: `<div style="width:12px;height:12px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 1px 3px rgba(0,0,0,.4)"></div>`,
    className: "",
    iconSize: [12, 12],
    iconAnchor: [6, 6],
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
      {withLocation.map((item) => (
        <Marker
          key={item.id}
          position={[item.lat!, item.lng!]}
          icon={markerIcon(item.gps_confidence)}
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
    </MapContainer>
  );
}
