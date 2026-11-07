// Leaflet stacks markers by their pixel row (a marker's z-index is its y plus this offset), so a
// lower marker covers a higher one. Only markers a few pixels apart overlap, so 1000 lifts the
// marker the user is looking for (the current item, the picked position, the actual location)
// over every marker it touches.
export const TOP_MARKER_Z_INDEX_OFFSET = 1000;
