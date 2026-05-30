import { useState } from "react";
import { MapPage } from "./pages/MapPage";
import { TripsPage } from "./pages/TripsPage";
import { GamePage } from "./pages/GamePage";
import { UploadPage } from "./pages/UploadPage";
import { Map, Images, Gamepad2, Upload } from "lucide-react";
import { cn } from "./lib/utils";

type Tab = "map" | "trips" | "game" | "upload";

const tabs: { id: Tab; label: string; icon: React.ElementType }[] = [
  { id: "map", label: "Map", icon: Map },
  { id: "trips", label: "Trips", icon: Images },
  { id: "game", label: "Game", icon: Gamepad2 },
  { id: "upload", label: "Upload", icon: Upload },
];

export default function App() {
  const [active, setActive] = useState<Tab>("map");

  return (
    <div className="flex h-screen flex-col bg-background">
      <header className="flex items-center gap-2 border-b px-4 py-2">
        <span className="text-lg font-semibold tracking-tight">GeoGallery</span>
        <nav className="ml-4 flex gap-1">
          {tabs.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => setActive(id)}
              className={cn(
                "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                active === id
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              )}
            >
              <Icon className="h-4 w-4" />
              {label}
            </button>
          ))}
        </nav>
      </header>

      <main className="flex-1 overflow-hidden">
        {active === "map" && <MapPage />}
        {active === "trips" && <TripsPage />}
        {active === "game" && <GamePage />}
        {active === "upload" && <UploadPage />}
      </main>
    </div>
  );
}
