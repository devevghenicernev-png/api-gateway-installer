import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { AuthProvider } from "./auth/auth-context";
import { Dashboard } from "./Dashboard";
import { ThemeProvider } from "./components/layout/ThemeProvider";

// Single QueryClient for the whole app. Defaults reflect the dashboard
// usage pattern: most data refreshes on a 10s poll, but we trust the
// cache for 5s so rapid panel jumps don't re-fire requests. Mutations
// can override per-call via meta.invalidates.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: true,
      refetchInterval: 10_000,
      retry: (failureCount, err: unknown) => {
        // 4xx errors are operator/auth issues — don't hammer.
        if (typeof err === "object" && err && "status" in err) {
          const s = (err as { status: number }).status;
          if (s >= 400 && s < 500) return false;
        }
        return failureCount < 2;
      },
    },
    mutations: {
      retry: false,
    },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <AuthProvider>
          <Dashboard />
          <Toaster
            position="bottom-right"
            theme="system"
            richColors
            closeButton
            // Pre-styled by shadcn theme via CSS vars; richColors gives
            // semantic backgrounds so success/error/warning don't all
            // render identical.
            toastOptions={{ className: "font-sans" }}
          />
        </AuthProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
