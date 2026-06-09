declare module "@changey/react-leaflet-markercluster" {
  import type { MarkerClusterGroupOptions } from "leaflet.markercluster";
  import type { DivIcon, Icon } from "leaflet";
  import type { ReactNode } from "react";

  interface MarkerClusterGroupProps extends MarkerClusterGroupOptions {
    children?: ReactNode;
    chunkedLoading?: boolean;
    iconCreateFunction?: (cluster: { getChildCount(): number }) => DivIcon | Icon;
  }

  const MarkerClusterGroup: React.FC<MarkerClusterGroupProps>;
  export default MarkerClusterGroup;
}

declare module "*.css";
