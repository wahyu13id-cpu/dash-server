import { memo, useCallback, useEffect, useState, type PointerEvent as ReactPointerEvent } from "react";
import { createPortal } from "react-dom";

import { api } from "../lib/api";
import {
  floatingTerminals,
  useFloatingTerminals,
  TITLE_BAR_HEIGHT,
  WINDOW_Z_BASE,
  type FloatTerminal,
} from "../lib/floatingTerminals";
import { TerminalPane, type PaneStatus, type TerminalSessionStore } from "./TerminalPane";
import { IconClose, IconMinus, IconTerminal } from "./Icons";

// The store adapter for floating windows: each window keeps its own session id,
// so a window and a terminal-page tab never end up sharing one PTY.
const floatSessions: TerminalSessionStore = {
  namespace: "float",
  sessionId: (tabId) => floatingTerminals.get(tabId)?.sessionId ?? "",
  remember: (tabId, sessionId, label) => floatingTerminals.remember(tabId, sessionId, label),
  forget: (tabId) => floatingTerminals.forget(tabId),
};

const STATUS_COLOR: Record<PaneStatus, string> = {
  connecting: "var(--warning, #fbbf24)",
  open: "var(--success, #4ade80)",
  error: "var(--danger, #f87171)",
  closed: "var(--text-dim, #64748b)",
};

const STATUS_TITLE: Record<PaneStatus, string> = {
  connecting: "menghubungkan",
  open: "terhubung",
  error: "gagal",
  closed: "sesi berakhir",
};

/**
 * FloatingTerminalLayer renders every open terminal window. It lives in the
 * layout, so the windows stay put while the dashboard is navigated — the PTY
 * sessions keep running the whole time.
 */
export function FloatingTerminalLayer() {
  const windows = useFloatingTerminals();

  // Keep windows reachable when the viewport shrinks (rotation, window resize).
  useEffect(() => {
    const onResize = () => floatingTerminals.clampAll(window.innerWidth, window.innerHeight);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  if (windows.length === 0) return null;

  return createPortal(
    <div className="float-layer" aria-label="Terminal mengambang">
      {windows.map((win, index) => (
        <FloatingWindow key={win.id} win={win} z={WINDOW_Z_BASE + index} />
      ))}
    </div>,
    document.body,
  );
}

const FloatingWindow = memo(function FloatingWindow({ win, z }: { win: FloatTerminal; z: number }) {
  const [status, setStatus] = useState<PaneStatus>("connecting");
  const onStatus = useCallback((_tabId: string, next: PaneStatus) => setStatus(next), []);

  const focusWindow = useCallback(() => floatingTerminals.focus(win.id), [win.id]);

  const closeWindow = useCallback(() => {
    const id = win.sessionId || floatingTerminals.get(win.id)?.sessionId || "";
    // Drop the window first: the UI must respond even if the session is already
    // gone on the server.
    floatingTerminals.close(win.id);
    if (id) void api.deleteTerminalSession(id).catch(() => undefined);
  }, [win.id, win.sessionId]);

  // Dragging the title bar moves the window, exactly like a desktop window.
  const startDrag = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      if (event.button !== 0) return;
      if ((event.target as HTMLElement).closest("button")) return;
      focusWindow();

      const bar = event.currentTarget;
      const startX = event.clientX;
      const startY = event.clientY;
      const originX = win.x;
      const originY = win.y;

      bar.setPointerCapture(event.pointerId);
      const onMove = (move: PointerEvent) => {
        floatingTerminals.move(win.id, originX + (move.clientX - startX), originY + (move.clientY - startY));
      };
      const onEnd = (end: PointerEvent) => {
        if (bar.hasPointerCapture(end.pointerId)) bar.releasePointerCapture(end.pointerId);
        bar.removeEventListener("pointermove", onMove);
        bar.removeEventListener("pointerup", onEnd);
        bar.removeEventListener("pointercancel", onEnd);
      };
      bar.addEventListener("pointermove", onMove);
      bar.addEventListener("pointerup", onEnd);
      bar.addEventListener("pointercancel", onEnd);
    },
    [focusWindow, win.id, win.x, win.y],
  );

  const startResize = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      if (event.button !== 0) return;
      event.stopPropagation();
      focusWindow();

      const grip = event.currentTarget;
      const startX = event.clientX;
      const startY = event.clientY;
      const originW = win.w;
      const originH = win.h;

      grip.setPointerCapture(event.pointerId);
      const onMove = (move: PointerEvent) => {
        floatingTerminals.resize(win.id, originW + (move.clientX - startX), originH + (move.clientY - startY));
      };
      const onEnd = (end: PointerEvent) => {
        if (grip.hasPointerCapture(end.pointerId)) grip.releasePointerCapture(end.pointerId);
        grip.removeEventListener("pointermove", onMove);
        grip.removeEventListener("pointerup", onEnd);
        grip.removeEventListener("pointercancel", onEnd);
      };
      grip.addEventListener("pointermove", onMove);
      grip.addEventListener("pointerup", onEnd);
      grip.addEventListener("pointercancel", onEnd);
    },
    [focusWindow, win.id, win.w, win.h],
  );

  return (
    <section
      className={`float-win ${win.minimized ? "float-win--min" : ""}`}
      style={{
        left: win.x,
        top: win.y,
        width: win.w,
        // +2 for the 1px top and bottom borders (border-box sizing).
        height: win.minimized ? TITLE_BAR_HEIGHT + 2 : win.h,
        zIndex: z,
      }}
      onPointerDown={focusWindow}
      role="dialog"
      aria-label={`Terminal ${win.label}`}
    >
      <div
        className="float-win__bar"
        onPointerDown={startDrag}
        onDoubleClick={() => floatingTerminals.toggleMaximize(win.id)}
      >
        <span className="float-win__dot" style={{ background: STATUS_COLOR[status] }} title={STATUS_TITLE[status]} />
        <span className="float-win__title" title={win.label}>
          <IconTerminal width={14} height={14} />
          {win.label}
        </span>
        <span className="float-win__actions">
          <button
            type="button"
            className="float-win__btn"
            onClick={() => floatingTerminals.toggleMinimize(win.id)}
            title={win.minimized ? "Kembalikan" : "Perkecil"}
            aria-label={win.minimized ? "Kembalikan jendela terminal" : "Perkecil jendela terminal"}
          >
            <IconMinus width={14} height={14} />
          </button>
          <button
            type="button"
            className="float-win__btn float-win__btn--close"
            onClick={closeWindow}
            title="Tutup dan akhiri sesi"
            aria-label="Tutup jendela terminal"
          >
            <IconClose width={14} height={14} />
          </button>
        </span>
      </div>

      <div className="float-win__body">
        <TerminalPane tab={win} visible={!win.minimized} onStatus={onStatus} store={floatSessions} />
      </div>

      <div
        className="float-win__grip"
        onPointerDown={startResize}
        title="Tarik untuk mengubah ukuran"
        aria-hidden="true"
      />
    </section>
  );
});
