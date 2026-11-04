import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

// Without a boundary, one exception while rendering (a Leaflet or three.js error in a fullscreen
// view, say) unmounts the whole React root and leaves a blank page. This shows a fallback in place
// of its children instead; the header and the other tabs keep working. "Try again" mounts the
// children afresh.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Render failed:", error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div role="alert" className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <p className="text-sm text-muted-foreground">Something went wrong while showing this page.</p>
        <button
          type="button"
          onClick={() => this.setState({ error: null })}
          className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent hover:text-accent-foreground touch:min-h-11"
        >
          Try again
        </button>
      </div>
    );
  }
}
