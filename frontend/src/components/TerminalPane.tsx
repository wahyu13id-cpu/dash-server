import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

import { api, terminalSocketURL } from "../lib/api";
import { terminalTabs, type TerminalTab } from "../lib/terminalTabs";
import { TerminalStatsStrip } from "./TerminalStatsStrip";

export type PaneStatus = "connecting" | "open" | "closed" | "error";

// The session cap is the one failure a user can actually act on, so it is
// spelled out instead of showing the raw server wording.
const SESSION_LIMIT_HINT =
  "Batas jumlah sesi terminal tercapai. Tutup tab terminal yang tidak dipakai " +
  "(atau tekan \u201cTutup semua sesi\u201d), lalu pilih \u201cCoba lagi\u201d.";

function describeSessionError(message: string): string {
  if (/limit/i.test(message)) return SESSION_LIMIT_HINT;
  return message;
}

/**
 * TerminalSessionStore is where a pane keeps its server-side session id. The
 * terminal page uses its tab list, a floating window uses its own record, so
 * both can render the same pane without sharing sessions.
 */
export interface TerminalSessionStore {
  /** Namespaces the per-tab cache so panes can never collide on a tab id. */
  namespace: string;
  /** Current PTY id for a tab, or "" when it has none yet. */
  sessionId: (tabId: string) => string;
  /** Records the PTY id (and the label the server gave it). */
  remember: (tabId: string, sessionId: string, label: string) => void;
  /** Drops the PTY id so the next mount creates a fresh one. */
  forget: (tabId: string) => void;
}

const tabStore: TerminalSessionStore = {
  namespace: "tabs",
  sessionId: (tabId) => terminalTabs.getTabs().find((t) => t.id === tabId)?.sessionId ?? "",
  remember: (tabId, sessionId, label) => terminalTabs.patch(tabId, { sessionId, label }),
  forget: (tabId) => terminalTabs.patch(tabId, { sessionId: "" }),
};

/**
 * ensureSession creates the server-side PTY once per tab. The promise is cached
 * per store + tab id so React's StrictMode double-mount (and any quick remount)
 * cannot spawn two sessions for the same tab.
 */
const inFlight = new Map<string, Promise<string>>();

function ensureSession(
  tab: TerminalTab,
  cols: number,
  rows: number,
  store: TerminalSessionStore,
): Promise<string> {
  // The store is the source of truth: a tab that survived a remount already has
  // a session id and must simply be re-attached.
  const existing = store.sessionId(tab.id);
  if (existing) return Promise.resolve(existing);

  const key = `${store.namespace}:${tab.id}`;
  const pending = inFlight.get(key);
  if (pending) return pending;

  const promise = api
    .createTerminalSession(tab.kind, tab.target, cols || 80, rows || 24, tab.dir ?? "")
    .then((session) => {
      store.remember(
        tab.id,
        session.id,
        session.kind === "ssh" ? `ssh ${session.target}` : session.title || tab.dir || "shell",
      );
      return session.id;
    })
    .finally(() => {
      inFlight.delete(key);
    });

  inFlight.set(key, promise);
  return promise;
}

const TERM_THEME = {
  background: "#0b1120",
  foreground: "#dbe4f3",
  cursor: "#7dd3fc",
  selectionBackground: "#334155",
  black: "#0b1120",
  brightBlack: "#64748b",
  red: "#f87171",
  green: "#4ade80",
  yellow: "#fbbf24",
  blue: "#60a5fa",
  magenta: "#c084fc",
  cyan: "#22d3ee",
  white: "#e2e8f0",
};

interface TerminalPaneProps {
  tab: TerminalTab;
  visible: boolean;
  onStatus: (tabId: string, status: PaneStatus, exitCode: number | null) => void;
  /** Defaults to the terminal page's tab list. */
  store?: TerminalSessionStore;
}

/**
 * TerminalPane owns one xterm instance and one websocket for a single tab. It
 * never deletes the session on unmount: leaving the page keeps the PTY alive,
 * and closing the tab does the deletion explicitly.
 */
export function TerminalPane({ tab, visible, onStatus, store = tabStore }: TerminalPaneProps) {
  const [status, setStatus] = useState<PaneStatus>("connecting");
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [generation, setGeneration] = useState(0);
  // Mirrors the tab's session id so the status strip can start polling as soon
  // as the PTY exists, without depending on the tab store's reactivity.
  const [sessionId, setSessionId] = useState(tab.sessionId ?? "");

  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const onStatusRef = useRef(onStatus);
  onStatusRef.current = onStatus;
  // Read through a ref: the store is a fresh object on every parent render, and
  // building the terminal again for that would drop the live websocket.
  const storeRef = useRef(store);
  storeRef.current = store;

  const report = useCallback((next: PaneStatus, code: number | null) => {
    setStatus(next);
    setExitCode(code);
    onStatusRef.current(tab.id, next, code);
  }, [tab.id]);

  // One terminal per pane, created once per mount/generation.
  useEffect(() => {
    let disposed = false;
    setError("");
    report("connecting", null);

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: '"JetBrains Mono", "Fira Code", ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.3,
      scrollback: 10000,
      convertEol: false,
      theme: TERM_THEME,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    const container = containerRef.current;
    if (container) {
      term.open(container);
      try {
        fit.fit();
      } catch {
        /* container not measurable yet */
      }
    }
    termRef.current = term;
    fitRef.current = fit;

    const onData = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data }));
      }
    });

    const observer = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        return;
      }
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    });
    if (container) observer.observe(container);

    const connect = (id: string) => {
      wsRef.current?.close();
      const ws = new WebSocket(terminalSocketURL(id));
      wsRef.current = ws;

      ws.onopen = () => {
        if (disposed) return;
        report("open", null);
        try {
          fit.fit();
        } catch {
          /* ignore */
        }
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      };

      ws.onmessage = (event) => {
        let msg: { type?: string; data?: string; code?: number | null; message?: string };
        try {
          msg = JSON.parse(event.data as string);
        } catch {
          return;
        }
        switch (msg.type) {
          case "output":
            if (msg.data) term.write(msg.data);
            break;
          case "exit":
            report("closed", msg.code ?? 0);
            break;
          case "error":
            term.writeln(`\r\n\x1b[31m${msg.message ?? "kesalahan"}\x1b[0m`);
            break;
          default:
            break;
        }
      };

      ws.onerror = () => {
        if (disposed) return;
        setError("Koneksi terminal gagal.");
        report("error", null);
      };

      ws.onclose = () => {
        if (disposed) return;
        report("closed", null);
      };
    };

    void (async () => {
      try {
        const id = await ensureSession(tab, term.cols, term.rows, storeRef.current);
        if (disposed) return;
        setSessionId(id);
        connect(id);
      } catch (err) {
        if (disposed) return;
        const message = describeSessionError(
          err instanceof Error ? err.message : "Gagal membuka sesi.",
        );
        term.writeln(`\r\n\x1b[31m${message}\x1b[0m`);
        setError(message);
        report("error", null);
      }
    })();

    return () => {
      disposed = true;
      observer.disconnect();
      onData.dispose();
      wsRef.current?.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
    // tab.dir is stable for a tab's lifetime, and store is read through a ref;
    // neither should ever rebuild a live terminal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab.id, tab.kind, tab.target, generation]);

  // A pane that becomes visible must re-measure: xterm cannot size itself while
  // its container is hidden.
  useEffect(() => {
    if (!visible) return;
    const id = window.requestAnimationFrame(() => {
      try {
        fitRef.current?.fit();
      } catch {
        return;
      }
      const ws = wsRef.current;
      const term = termRef.current;
      if (ws && term && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
      term?.focus();
    });
    return () => window.cancelAnimationFrame(id);
  }, [visible, status]);

  const restart = useCallback(() => {
    // A restart means a brand new PTY for this tab.
    storeRef.current.forget(tab.id);
    setSessionId("");
    setExitCode(null);
    setGeneration((g) => g + 1);
  }, [tab.id]);

  return (
    <div className={`term-pane ${visible ? "term-pane--active" : ""}`} hidden={!visible}>
      {visible && (
        <TerminalStatsStrip sessionId={sessionId} kind={tab.kind} target={tab.target} />
      )}
      {status === "error" && (
        <div className="term-pane__overlay">
          <div className="term-pane__overlay-card">
            <strong>Gagal membuka sesi</strong>
            <p>{error || "Terjadi kesalahan."}</p>
            <button className="btn btn--secondary btn--sm" type="button" onClick={restart}>
              <span>Coba lagi</span>
            </button>
          </div>
        </div>
      )}
      {status === "closed" && (
        <div className="term-pane__overlay term-pane__overlay--soft">
          <div className="term-pane__overlay-card">
            <strong>Sesi berakhir</strong>
            <p>Kode keluar {exitCode ?? 0}.</p>
            <button className="btn btn--secondary btn--sm" type="button" onClick={restart}>
              <span>Mulai sesi baru</span>
            </button>
          </div>
        </div>
      )}
      <div ref={containerRef} className="terminal-container" aria-label={`Terminal ${tab.target || "shell"}`} />
    </div>
  );
}
