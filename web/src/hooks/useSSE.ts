import { useEffect, useRef, useState } from "react";

// Connection state shared with the SSE indicator in TopBar + Live logs.
export type SSEStatus = "connecting" | "open" | "reconnecting" | "closed";

export type SSEHandlers = Partial<{
  [eventName: string]: (data: unknown, ev: MessageEvent) => void;
}>;

// useSSE opens a single EventSource subscribed to the given topics and
// dispatches each typed event to the handler with the matching name.
// We expose connection status so the UI can render a "live/reconnecting"
// indicator distinct from a missing-events empty state.
//
// EventSource will auto-reconnect by spec; we surface that as
// "reconnecting" via the onerror callback, then back to "open" on the
// next message arrival (we don't get an "open" event after the implicit
// reconnect — the EventSource API doesn't fire one).
export function useSSE(topics: string[], handlers: SSEHandlers, enabled: boolean = true) {
  const [status, setStatus] = useState<SSEStatus>("connecting");
  const [lastEventAt, setLastEventAt] = useState<number | null>(null);
  // Stash handlers in a ref so the EventSource doesn't reopen on every
  // parent re-render (which would otherwise drop in-flight events).
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  useEffect(() => {
    if (!enabled || topics.length === 0) {
      setStatus("closed");
      return;
    }
    const qs = topics.map((t) => `topic=${encodeURIComponent(t)}`).join("&");
    const es = new EventSource(`events?${qs}`, { withCredentials: false });
    setStatus("connecting");

    es.onopen = () => setStatus("open");
    es.onerror = () => setStatus("reconnecting");

    const listeners: Array<{ event: string; fn: (e: Event) => void }> = [];
    for (const ev of Object.keys(handlersRef.current)) {
      const fn = (e: Event) => {
        const me = e as MessageEvent<string>;
        try {
          const data = JSON.parse(me.data);
          setLastEventAt(Date.now());
          // Re-read from ref so updates after the listener registered
          // pick up the latest closure.
          handlersRef.current[ev]?.(data, me);
        } catch {
          // Malformed payload (truncated/corrupted SSE chunk) — drop.
        }
      };
      es.addEventListener(ev, fn);
      listeners.push({ event: ev, fn });
    }

    return () => {
      for (const { event, fn } of listeners) es.removeEventListener(event, fn);
      es.close();
      setStatus("closed");
    };
    // Only re-subscribe when the topic set actually changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topics.join(","), enabled]);

  return { status, lastEventAt };
}
