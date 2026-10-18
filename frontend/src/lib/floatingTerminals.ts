// Terminal windows that float above the app the way desktop terminals do.
//
// They are deliberately independent of the terminal page's tab store: a window
// owns its own PTY, so closing the window ends exactly that session and nothing
// else. The list is mirrored into sessionStorage for the same reason the tab
// list is — a reload should re-attach to the sessions that are still running
// instead of leaving them behind with no way back.

import { useSyncExternalStore } from "react";

export interface FloatTerminal {
  id: string;
  kind: "shell" | "ssh";
  target: string;
  dir: string;
  label: string;
  sessionId: string;
  x: number;
  y: number;
  w: number;
  h: number;
  minimized: boolean;
  /** Saved geometry while the window is maximized; null when it is not. */
  restore: { x: number; y: number; w: number; h: number } | null;
}

/** Height of the title bar; a minimized window keeps exactly this much. */
export const TITLE_BAR_HEIGHT = 34;
export const MIN_WINDOW_W = 340;
export const MIN_WINDOW_H = 180;
/** A cap so a stuck click cannot open a wall of xterm instances. */
export const MAX_FLOAT_WINDOWS = 6;
/** Keep in sync with the --z-float layer in styles/global.css. */
export const WINDOW_Z_BASE = 62;

const STORAGE_KEY = "dashboard.floats.v1";
const STORAGE_VERSION = 1;
const CASCADE_STEP = 30;
const MARGIN = 12;
/** How much of a window must stay reachable while dragging it off an edge. */
const KEEP_VISIBLE = 120;

export interface OpenTerminalOptions {
  kind?: "shell" | "ssh";
  target?: string;
  dir?: string;
  label?: string;
}

function viewport(): { vw: number; vh: number } {
  if (typeof window === "undefined") return { vw: 1280, vh: 800 };
  return { vw: window.innerWidth, vh: window.innerHeight };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), Math.max(min, max));
}

// Dragging is clamped, not blocked: a window may hang off an edge as long as its
// title bar (or a slice of the body) is still reachable to pull it back.
function clampX(x: number, w: number, vw: number): number {
  return clamp(x, -w + KEEP_VISIBLE, vw - KEEP_VISIBLE);
}

function clampY(y: number, vh: number): number {
  return clamp(y, 0, vh - TITLE_BAR_HEIGHT - 4);
}

function cascadeGeometry(index: number, vw: number, vh: number): { x: number; y: number; w: number; h: number } {
  if (vw < 720) {
    // A phone has no room for overlapping windows: fill the screen instead.
    return {
      x: 0,
      y: 0,
      w: Math.max(MIN_WINDOW_W, vw),
      h: Math.max(MIN_WINDOW_H, vh - TITLE_BAR_HEIGHT - MARGIN),
    };
  }
  const w = Math.min(880, Math.max(420, Math.round(vw * 0.5)));
  const h = Math.min(520, Math.max(260, Math.round(vh * 0.46)));
  const offset = CASCADE_STEP * (index % 5);
  return { x: clampX(64 + offset, w, vw), y: clampY(84 + offset, vh), w, h };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function parseWindow(value: unknown): FloatTerminal | null {
  if (!isRecord(value)) return null;
  const { id } = value;
  if (typeof id !== "string" || id === "") return null;

  const num = (raw: unknown, fallback: number) =>
    typeof raw === "number" && Number.isFinite(raw) ? raw : fallback;
  const geometry = (raw: unknown, fallbackX: number, fallbackY: number, fallbackW: number, fallbackH: number) => {
    const source = isRecord(raw) ? raw : {};
    return {
      x: num(source.x, fallbackX),
      y: num(source.y, fallbackY),
      w: Math.max(MIN_WINDOW_W, num(source.w, fallbackW)),
      h: Math.max(MIN_WINDOW_H, num(source.h, fallbackH)),
    };
  };

  return {
    id,
    kind: value.kind === "ssh" ? "ssh" : "shell",
    target: typeof value.target === "string" ? value.target : "",
    dir: typeof value.dir === "string" ? value.dir : "",
    label: typeof value.label === "string" ? value.label : "shell",
    sessionId: typeof value.sessionId === "string" ? value.sessionId : "",
    ...geometry(value, 64, 84, 720, 420),
    minimized: value.minimized === true,
    restore: isRecord(value.restore) ? geometry(value.restore, 64, 84, 720, 420) : null,
  };
}

function readPersisted(): FloatTerminal[] {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.v !== STORAGE_VERSION || !Array.isArray(parsed.windows)) {
      return [];
    }
    return parsed.windows
      .map(parseWindow)
      .filter((win): win is FloatTerminal => win !== null)
      .slice(0, MAX_FLOAT_WINDOWS);
  } catch {
    return [];
  }
}

let windows: FloatTerminal[] = readPersisted();
let seq = 0;
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

function persist() {
  try {
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ v: STORAGE_VERSION, windows } satisfies Record<string, unknown>),
    );
  } catch {
    // Storage disabled or full: the windows simply do not survive a reload.
  }
}

function nextID(): string {
  seq += 1;
  return `float-${Date.now().toString(36)}-${seq}`;
}

function patch(id: string, patchValue: Partial<FloatTerminal>) {
  let changed = false;
  const next = windows.map((win) => {
    if (win.id !== id) return win;
    changed = true;
    return { ...win, ...patchValue };
  });
  if (!changed) return;
  windows = next;
  emit();
  persist();
}

export const floatingTerminals = {
  subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },

  getWindows(): FloatTerminal[] {
    return windows;
  },

  get(id: string): FloatTerminal | null {
    return windows.find((win) => win.id === id) ?? null;
  },

  /**
   * open adds a window, or focuses the one that is already pointed at the same
   * directory. It answers null when the cap is reached, so the caller can say so
   * instead of silently doing nothing.
   */
  open(options: OpenTerminalOptions = {}): string | null {
    const kind = options.kind === "ssh" ? "ssh" : "shell";
    const target = kind === "ssh" ? (options.target ?? "") : "";
    const dir = kind === "shell" ? (options.dir ?? "") : "";

    const existing = windows.find(
      (win) => win.kind === kind && win.target === target && win.dir === dir,
    );
    if (existing) {
      if (existing.minimized) patch(existing.id, { minimized: false });
      floatingTerminals.focus(existing.id);
      return existing.id;
    }

    if (windows.length >= MAX_FLOAT_WINDOWS) return null;

    const { vw, vh } = viewport();
    const win: FloatTerminal = {
      id: nextID(),
      kind,
      target,
      dir,
      label: options.label?.trim() || (kind === "ssh" ? `ssh ${target}` : dir || "shell"),
      sessionId: "",
      ...cascadeGeometry(windows.length, vw, vh),
      minimized: false,
      restore: null,
    };
    windows = [...windows, win];
    emit();
    persist();
    return win.id;
  },

  close(id: string) {
    const next = windows.filter((win) => win.id !== id);
    if (next.length === windows.length) return;
    windows = next;
    emit();
    persist();
  },

  /** focus raises a window to the top of the stack. It is a no-op when it already is. */
  focus(id: string) {
    const index = windows.findIndex((win) => win.id === id);
    if (index < 0 || index === windows.length - 1) return;
    const target = windows[index];
    windows = [...windows.slice(0, index), ...windows.slice(index + 1), target];
    emit();
    persist();
  },

  move(id: string, x: number, y: number) {
    const win = floatingTerminals.get(id);
    if (!win) return;
    const { vw, vh } = viewport();
    patch(id, { x: clampX(x, win.w, vw), y: clampY(y, vh) });
  },

  resize(id: string, w: number, h: number) {
    const win = floatingTerminals.get(id);
    if (!win) return;
    const { vw, vh } = viewport();
    const nextW = clamp(w, MIN_WINDOW_W, Math.max(MIN_WINDOW_W, vw - MARGIN * 2));
    const nextH = clamp(h, MIN_WINDOW_H, Math.max(MIN_WINDOW_H, vh - TITLE_BAR_HEIGHT - MARGIN * 2));
    // Growing near an edge slides the window back into view instead of pushing
    // it past the screen.
    const x = win.x + nextW > vw - MARGIN ? clampX(vw - nextW - MARGIN, nextW, vw) : win.x;
    const y = win.y + nextH > vh - MARGIN ? clampY(vh - nextH - MARGIN, vh) : win.y;
    patch(id, { w: nextW, h: nextH, x, y });
  },

  /** toggleMaximize fills the viewport, remembering where the window was. */
  toggleMaximize(id: string) {
    const win = floatingTerminals.get(id);
    if (!win) return;
    if (win.restore) {
      patch(id, { ...win.restore, restore: null, minimized: false });
      return;
    }
    const { vw, vh } = viewport();
    const geometry = { x: win.x, y: win.y, w: win.w, h: win.h };
    patch(id, {
      restore: geometry,
      x: MARGIN,
      y: MARGIN,
      w: Math.max(MIN_WINDOW_W, vw - MARGIN * 2),
      h: Math.max(MIN_WINDOW_H, vh - MARGIN * 2),
      minimized: false,
    });
  },

  toggleMinimize(id: string) {
    const win = floatingTerminals.get(id);
    if (!win) return;
    patch(id, { minimized: !win.minimized });
  },

  /** remember stores the server-side session id once the PTY exists. */
  remember(id: string, sessionId: string, label: string) {
    patch(id, { sessionId, label });
  },

  /** forget drops the session id, which makes the next mount start a new PTY. */
  forget(id: string) {
    patch(id, { sessionId: "" });
  },

  /** clampAll keeps every window reachable after the viewport changes size. */
  clampAll(vw: number, vh: number) {
    let changed = false;
    const next = windows.map((win) => {
      const x = clampX(win.x, win.w, vw);
      const y = clampY(win.y, vh);
      const w = Math.min(win.w, Math.max(MIN_WINDOW_W, vw - MARGIN));
      const h = Math.min(win.h, Math.max(MIN_WINDOW_H, vh - TITLE_BAR_HEIGHT - MARGIN));
      if (x === win.x && y === win.y && w === win.w && h === win.h) return win;
      changed = true;
      return { ...win, x, y, w, h };
    });
    if (!changed) return;
    windows = next;
    emit();
    persist();
  },
};

export function useFloatingTerminals(): FloatTerminal[] {
  return useSyncExternalStore(
    floatingTerminals.subscribe,
    floatingTerminals.getWindows,
    floatingTerminals.getWindows,
  );
}
