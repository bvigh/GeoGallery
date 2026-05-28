import { MapContainer, TileLayer, Marker, Popup } from "react-leaflet";
import type { MediaItem } from "../../types";
import "leaflet/dist/leaflet.css";
import L from "leaflet";

// Fix default marker icons in Vite/webpack builds
delete (L.Icon.Default.prototype as unknown as Record<string, unknown>)._getIconUrl;
L.Icon.Default.mergeOptions({
  iconRetinaUrl: "https://unpkg.com/leaflet@1.9.4/dist/images/marker-icon-2x.png",
  iconUrl: "https://unpkg.com/leaflet@1.9.4/dist/images/marker-icon.png",
  shadowUrl: "https://unpkg.com/leaflet@1.9.4/dist/images/marker-shadow.png",
});

interface Props {
  items: MediaItem[];
  onSelect?: (item: MediaItem) => void;
}

export function MediaMap({ items, onSelect }: Props) {
  const withLocation = items.filter((m) => m.lat !== null && m.lng !== null);

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
