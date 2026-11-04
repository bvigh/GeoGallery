import { useState } from "react";
import { MapPage } from "@/pages/MapPage";
import { TripsPage } from "@/pages/TripsPage";
import { AlbumsPage } from "@/pages/AlbumsPage";
import { GamePage } from "@/pages/GamePage";
import { UploadPage } from "@/pages/UploadPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { AdminPage } from "@/pages/AdminPage";
import { AuthGate } from "@/components/auth/AuthGate";
import { ErrorBoundary } from "@/components/ErrorBoundary";
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

  // Below lg the tabs and Logout are icon-only (the label stays as their accessible name), so the
  // header never gets wider than a phone; a wider page would scroll and zoom out on touch.
  // relative: keeps the sr-only label inside the button, not in the nav's scroll area.
  const buttonClass =
    "relative flex shrink-0 items-center justify-center gap-1.5 rounded-md px-2.5 py-1.5 font-medium transition-colors lg:px-3 touch:min-h-11";
  const iconClass = "h-4 w-4 touch:h-5 touch:w-5";
  const name = user.display_name ?? user.email ?? "";

  return (
    // overflow-clip-safe: nothing inside the shell can make #root scroll and slide the header away.
    // h-full: #root is the viewport's height (100dvh, else 100%); h-dvh alone has no fallback.
    <div className="flex h-full flex-col overflow-clip-safe bg-background">
      <header className="flex items-center gap-1 border-b px-2 py-1 sm:gap-2 sm:px-4 lg:py-2 short:py-0">
        <span className="hidden shrink-0 text-lg font-semibold tracking-tight sm:inline">GeoGallery</span>
        {/* The padding and matching negative margins leave room for focus outlines, which the
            scroll container would otherwise clip. Below lg a strip that does not fit scrolls
            sideways; from lg the nav never shrinks (the labelled tabs fit from 1024px), so the
            user name gives way instead of hiding tabs behind a scrollbar-less scroller. */}
        <nav className="-mx-1 -my-1 flex min-w-0 gap-0.5 overflow-x-auto px-1 py-1 [scrollbar-width:none] sm:ml-3 sm:gap-1 lg:shrink-0 [&::-webkit-scrollbar]:hidden">
          {tabs.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              title={label}
              aria-current={active === id ? "page" : undefined}
              onClick={() => setActive(id)}
              className={cn(
                buttonClass,
                // 40px wide below sm, so an admin's seven tabs and Logout fit a 360px phone.
                "text-sm touch:min-w-10 sm:touch:min-w-11",
                active === id
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              )}
            >
              <Icon className={iconClass} aria-hidden="true" />
              <span className="sr-only lg:not-sr-only">{label}</span>
            </button>
          ))}
        </nav>
        <div className="ml-auto flex min-w-0 items-center gap-2 text-sm lg:gap-3">
          <span title={name} className="hidden min-w-0 truncate text-muted-foreground lg:inline">
            {name}
          </span>
          {logoutError && (
            <span role="alert" className="max-w-28 text-xs leading-tight text-red-500 lg:max-w-none lg:text-sm">
              Logout failed, try again
            </span>
          )}
          <button
            title="Logout"
            onClick={logout}
            className={cn(buttonClass, "text-muted-foreground hover:bg-accent hover:text-accent-foreground touch:min-w-11")}
          >
            <LogOut className={iconClass} aria-hidden="true" />
            <span className="sr-only lg:not-sr-only">Logout</span>
          </button>
        </div>
      </header>

      {/* Clipped, not overflow-hidden: a hidden box still scrolls when focus() or scrollIntoView()
          reaches content below its edge, which slid the whole map page up on sideways phones.
          min-h-0: a clipped box is no scroll container, so as a flex item it would otherwise grow
          to its content's height and break the pages' own scrolling. */}
      <main className="min-h-0 flex-1 overflow-clip-safe">
        {/* Hidden rather than unmounted: rebuilding the map and reloading every marker on each
            visit is slow. Each page has an error boundary, so a crash in it or in one of its
            fullscreen views shows a fallback there, not a blank app. */}
        <div className={cn("h-full", active !== "map" && "hidden")}>
          <ErrorBoundary>
            <MapPage active={active === "map"} />
          </ErrorBoundary>
        </div>
        {/* Keyed by the tab: opening another one starts with a fresh boundary. */}
        <ErrorBoundary key={active}>
          {active === "trips" && <TripsPage />}
          {active === "albums" && <AlbumsPage />}
          {active === "game" && <GamePage />}
          {active === "upload" && <UploadPage />}
          {active === "settings" && <SettingsPage />}
          {active === "admin" && user.role === "admin" && <AdminPage />}
        </ErrorBoundary>
      </main>
    </div>
  );
}

export default function App() {
  // The last resort, for the shell itself; the pages have their own boundaries.
  return (
    <ErrorBoundary>
      <AuthGate>
        <Shell />
      </AuthGate>
    </ErrorBoundary>
  );
}
