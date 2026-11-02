import { useEffect, useRef, useState } from "react";
import { MapContainer, TileLayer, useMap } from "react-leaflet";
import type { MediaItem } from "../../types";
import "leaflet/dist/leaflet.css";
import "leaflet.markercluster/dist/MarkerCluster.css";
import "leaflet.markercluster/dist/MarkerCluster.Default.css";
import L from "leaflet";
import "leaflet.markercluster";

// One icon per look, shared by every marker: a new icon object would make Leaflet rebuild the
// marker's DOM element.
const icons = new Map<string, L.DivIcon>();

function markerIcon(item: MediaItem): L.DivIcon {
  const color =
    item.gps_confidence !== null && item.gps_confidence < 0.9 ? "#f97316" : "#3b82f6";
  const is360 = item.media_type === "photo_360" || item.media_type === "video_360";
  const key = `${color}/${is360}`;
  let icon = icons.get(key);
  if (!icon) {
    const size = is360 ? 16 : 12;
    const half = size / 2;
    const outline = is360 ? "outline:2px solid #a855f7;outline-offset:2px;" : "";
    icon = L.divIcon({
      html: `<div style="width:${size}px;height:${size}px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 1px 3px rgba(0,0,0,.4);${outline}"></div>`,
      className: "",
      iconSize: [size, size],
      iconAnchor: [half, half],
    });
    icons.set(key, icon);
  }
  return icon;
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

// Built with textContent: filenames come from users.
function popupContent(item: MediaItem): HTMLElement {
  const root = document.createElement("div");
  root.className = "text-sm";
  const name = document.createElement("p");
  name.className = "font-medium";
  name.textContent = item.filename;
  root.append(name);
  if (item.taken_at) {
    const date = document.createElement("p");
    date.className = "text-muted-foreground";
    date.textContent = new Date(item.taken_at).toLocaleDateString();
    root.append(date);
  }
  return root;
}

function ZoomControlBottomLeft() {
  const map = useMap();
  useEffect(() => {
    const control = L.control.zoom({ position: "bottomleft" });
    control.addTo(map);
    return () => { control.remove(); };
  }, [map]);
  return null;
}

// A map laid out while hidden (display: none) has size 0 until told otherwise.
function InvalidateSizeWhenVisible({ visible }: { visible: boolean }) {
  const map = useMap();
  useEffect(() => {
    if (visible) map.invalidateSize();
  }, [map, visible]);
  return null;
}

interface Props {
  items: MediaItem[];
  onSelect?: (item: MediaItem, peers: MediaItem[]) => void;
  // false while the map's page is hidden; becoming visible again re-measures the map.
  visible?: boolean;
}

interface Entry {
  item: MediaItem;
  marker: L.Marker;
}

// Owns the cluster layer imperatively instead of rendering one <Marker> per item: React-rendered
// markers get a new position array and icon on every render, and markercluster answers each
// setLatLng by taking the marker out of its cluster tree and putting it back. Here an items
// change touches only the markers that were added, removed or changed, in one bulk call each.
function ClusterLayer({ items, onSelect }: Omit<Props, "visible">) {
  const map = useMap();
  const entries = useRef(new Map<string, Entry>());
  const idOf = useRef(new WeakMap<L.Layer, string>());
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // removeLayers skips a marker that a chunked addLayers has not reached yet, and the batch then
  // adds it anyway. Such strays are spotted as they are added and removed when the chunk ends.
  const removed = useRef(new WeakSet<L.Layer>());
  const strays = useRef<L.Layer[]>([]);

  const [group] = useState(() => {
    const g = L.markerClusterGroup({
      chunkedLoading: true,
      iconCreateFunction: createClusterIcon,
      chunkProgress: () => {
        if (strays.current.length === 0) return;
        g.removeLayers(strays.current);
        strays.current = [];
      },
    });
    g.on("layeradd", (e) => {
      if (removed.current.has(e.layer)) strays.current.push(e.layer);
    });
    return g;
  });

  useEffect(() => {
    map.addLayer(group);
    return () => { map.removeLayer(group); };
  }, [map, group]);

  // Marker clicks bubble to the group. A click on a spiderfied marker selects it among the
  // markers of that spider, any other click on its own.
  useEffect(() => {
    let peers: MediaItem[] = [];
    const itemOf = (layer: L.Layer) => entries.current.get(idOf.current.get(layer) ?? "")?.item;

    function onSpiderfied(e: L.MarkerClusterSpiderfyEvent) {
      peers = e.markers.map(itemOf).filter((m): m is MediaItem => m !== undefined);
    }
    function onUnspiderfied() {
      // Delay one tick: unspiderfied fires before the marker click,
      // so the click handler must read peers before we clear it.
      setTimeout(() => { peers = []; }, 0);
    }
    function onClick(e: L.LeafletEvent) {
      const item = itemOf(e.propagatedFrom);
      if (!item) return;
      onSelectRef.current?.(item, peers.length > 0 ? peers : [item]);
    }

    group.on("spiderfied", onSpiderfied);
    group.on("unspiderfied", onUnspiderfied);
    group.on("click", onClick);
    return () => {
      group.off("spiderfied", onSpiderfied);
      group.off("unspiderfied", onUnspiderfied);
      group.off("click", onClick);
    };
  }, [group]);

  useEffect(() => {
    const next = new Map<string, MediaItem>();
    for (const item of items) {
      if (item.lat !== null && item.lng !== null) next.set(item.id, item);
    }

    const gone: L.Marker[] = [];
    for (const [id, entry] of entries.current) {
      if (!next.has(id)) {
        gone.push(entry.marker);
        removed.current.add(entry.marker);
        entries.current.delete(id);
      }
    }

    const added: L.Marker[] = [];
    for (const [id, item] of next) {
      const entry = entries.current.get(id);
      if (!entry) {
        const marker = L.marker([item.lat!, item.lng!], { icon: markerIcon(item) });
        const created = { item, marker };
        // A function, so the popup shows the entry's current item.
        marker.bindPopup(() => popupContent(created.item));
        idOf.current.set(marker, id);
        entries.current.set(id, created);
        added.push(marker);
        continue;
      }
      if (entry.item === item) continue;
      const prev = entry.item;
      entry.item = item;
      // The cluster group moves a marker between clusters on setLatLng: only for a real move.
      if (prev.lat !== item.lat || prev.lng !== item.lng) entry.marker.setLatLng([item.lat!, item.lng!]);
      const icon = markerIcon(item);
      if (entry.marker.options.icon !== icon) entry.marker.setIcon(icon);
    }

    if (gone.length > 0) {
      if (entries.current.size === 0) group.clearLayers();
      else group.removeLayers(gone);
    }
    if (added.length > 0) group.addLayers(added);
  }, [group, items]);

  return null;
}

export function MediaMap({ items, onSelect, visible = true }: Props) {
  return (
    <MapContainer
      center={[20, 0]}
      zoom={2}
      zoomControl={false}
      className="h-full w-full"
    >
      <ZoomControlBottomLeft />
      <InvalidateSizeWhenVisible visible={visible} />
      <TileLayer
        attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'
        url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
      />
      <ClusterLayer items={items} onSelect={onSelect} />
    </MapContainer>
  );
}
