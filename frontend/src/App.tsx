import { useState } from "react";
import { MapPage } from "@/pages/MapPage";
import { TripsPage } from "@/pages/TripsPage";
import { AlbumsPage } from "@/pages/AlbumsPage";
import { GamePage } from "@/pages/GamePage";
import { UploadPage } from "@/pages/UploadPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { AdminPage } from "@/pages/AdminPage";
import { AuthGate } from "@/components/auth/AuthGate";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { api } from "@/api/client";
import { Map, Images, AlbumIcon, Gamepad2, Upload, Settings, Shield, LogOut } from "lucide-react";
import { cn } from "@/lib/utils";

type Tab = "map" | "trips" | "albums" | "game" | "upload" | "settings" | "admin";

const baseTabs: { id: Tab; label: string; icon: React.ElementType }[] = [
  { id: "map", label: "Map", icon: Map },
  { id: "trips", label: "Trips", icon: Images },
  { id: "albums", label: "Albums", icon: AlbumIcon },
  { id: "game", label: "Game", icon: Gamepad2 },
  { id: "upload", label: "Upload", icon: Upload },
  { id: "settings", label: "Settings", icon: Settings },
];

const adminTab = { id: "admin" as const, label: "Admin", icon: Shield };

function Shell() {
  const user = useCurrentUser();
  const [active, setActive] = useState<Tab>("map");
  const [logoutError, setLogoutError] = useState(false);
  const tabs = user.role === "admin" ? [...baseTabs, adminTab] : baseTabs;

  const logout = () => {
    setLogoutError(false);
    api.logout().catch((e) => {
      console.error(e);
      setLogoutError(true);
    });
  };

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
        <div className="ml-auto flex items-center gap-3 text-sm">
          <span className="text-muted-foreground">{user.display_name ?? user.email ?? ""}</span>
          {logoutError && <span className="text-red-500">Logout failed, try again</span>}
          <button
            onClick={logout}
            className="flex items-center gap-1.5 rounded-md px-3 py-1.5 font-medium text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          >
            <LogOut className="h-4 w-4" />
            Logout
          </button>
        </div>
      </header>

      <main className="flex-1 overflow-hidden">
        {active === "map" && <MapPage />}
        {active === "trips" && <TripsPage />}
        {active === "albums" && <AlbumsPage />}
        {active === "game" && <GamePage />}
        {active === "upload" && <UploadPage />}
        {active === "settings" && <SettingsPage />}
        {active === "admin" && user.role === "admin" && <AdminPage />}
      </main>
    </div>
  );
}

export default function App() {
  return (
    <AuthGate>
      <Shell />
    </AuthGate>
  );
}
