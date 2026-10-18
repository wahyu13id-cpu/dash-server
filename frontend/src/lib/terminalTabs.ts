// Terminal tabs live in a module-level store so that navigating to another
// page does not close them. The PTY sessions themselves belong to the server
// and keep running until they are closed or hit the idle timeout; the tab list
// here only remembers what the browser should re-attach to when the terminal
// page is opened again.

import { useSyncExternalStore } from "react";

export type TabKind = "shell" | "ssh";

export interface TerminalTab {
  /** Stable client-side identity for the tab, independent of the session id. */
  id: string;
  kind: TabKind;
  /** SSH alias for kind "ssh". */
  target: string;
  /**
   * Starting directory for a local shell, used by "Buka di Terminal" from the
   * file manager. Empty means the dashboard's configured default.
   */
  dir: string;
  /** Server-side session id, filled in once the PTY has been created. */
  sessionId: string;
  /** Label shown on the tab; falls back to a kind-based default. */
  label: string;
}

// The tab list is mirrored into sessionStorage so that a reload attaches to the
// PTY sessions that are already running instead of creating a second set. That
// mirror is what keeps the session cap from filling up with orphans: without it
// every reload used to leave its sessions behind with no client and no way to
// get back to them.
const STORAGE_KEY = "dashboard.terminal.tabs.v1";
const STORAGE_VERSION = 1;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function parseTab(value: unknown): TerminalTab | null {
  if (!isRecord(value)) return null;
  const { id, kind, target, sessionId, label } = value;
  if (typeof id !== "string" || id === "") return null;
  if (kind !== "shell" && kind !== "ssh") return null;
  return {
    id,
    kind,
    target: typeof target === "string" ? target : "",
    dir: typeof value.dir === "string" ? value.dir : "",
    sessionId: typeof sessionId === "string" ? sessionId : "",
    label: typeof label === "string" ? label : "",
  };
}

function readPersisted(): { tabs: TerminalTab[]; activeId: string } {
  const empty = { tabs: [] as TerminalTab[], activeId: "" };
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return empty;
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.v !== STORAGE_VERSION || !Array.isArray(parsed.tabs)) {
      return empty;
    }
    const tabs: TerminalTab[] = [];
    for (const entry of parsed.tabs) {
      const tab = parseTab(entry);
      if (tab) tabs.push(tab);
    }
    const wanted = typeof parsed.activeId === "string" ? parsed.activeId : "";
    const activeId = tabs.some((tab) => tab.id === wanted) ? wanted : (tabs[0]?.id ?? "");
    return { tabs, activeId };
  } catch {
    return empty;
  }
}

function persist() {
  try {
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ v: STORAGE_VERSION, tabs, activeId } satisfies Record<string, unknown>),
    );
  } catch {
    // Storage disabled or full: the tabs simply do not survive a reload.
  }
}

const restored = readPersisted();
let tabs: TerminalTab[] = restored.tabs;
let activeId = restored.activeId;
// Remembered session ids are only trusted after they have been checked against
// the server; reconcileTabs does that and clears the flag.
let pendingRestore = restored.tabs.some((tab) => tab.sessionId !== "");
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

/** newShellTab builds a fresh local-shell tab, optionally in a directory. */
export function newShellTab(dir = ""): TerminalTab {
  return { id: newTabID(), kind: "shell", target: "", dir, sessionId: "", label: dir || "shell" };
}

/** tabsNeedRestore reports whether a reload left session ids to validate. */
export function tabsNeedRestore(): boolean {
  return pendingRestore;
}

/**
 * reconcileTabs keeps only the tabs whose PTY still exists on the server, so a
 * pane never attaches to a session id the server has already forgotten. Tabs
 * that are gone are dropped rather than re-created: the point of the restore is
 * to re-attach, and silently opening a batch of new sessions would refill the
 * cap. One fresh shell is opened when nothing survived.
 */
export function reconcileTabs(liveSessionIds: string[]) {
  pendingRestore = false;
  const live = new Set(liveSessionIds);
  const kept = tabs.filter((tab) => tab.sessionId !== "" && live.has(tab.sessionId));
  if (kept.length === 0) {
    const tab = newShellTab();
    tabs = [tab];
    activeId = tab.id;
  } else {
    tabs = kept;
    if (!kept.some((tab) => tab.id === activeId)) activeId = kept[0].id;
  }
  emit();
  persist();
}

export const terminalTabs = {
  getTabs: (): TerminalTab[] => tabs,
  getActiveId: (): string => activeId,

  subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },

  setTabs(next: TerminalTab[] | ((prev: TerminalTab[]) => TerminalTab[])) {
    tabs = typeof next === "function" ? next(tabs) : next;
    emit();
    persist();
  },

  setActive(id: string) {
    if (activeId === id) return;
    activeId = id;
    emit();
    persist();
  },

  add(tab: TerminalTab): TerminalTab {
    tabs = [...tabs, tab];
    activeId = tab.id;
    emit();
    persist();
    return tab;
  },

  patch(id: string, changes: Partial<TerminalTab>) {
    let changed = false;
    tabs = tabs.map((tab) => {
      if (tab.id !== id) return tab;
      changed = true;
      return { ...tab, ...changes };
    });
    if (changed) {
      emit();
      persist();
    }
  },

  remove(id: string) {
    tabs = tabs.filter((tab) => tab.id !== id);
    if (activeId === id) {
      activeId = tabs.length > 0 ? tabs[tabs.length - 1].id : "";
    }
    emit();
    persist();
  },

  clear() {
    tabs = [];
    activeId = "";
    emit();
    persist();
  },
};

export function useTerminalTabs(): { tabs: TerminalTab[]; activeId: string } {
  const snapTabs = useSyncExternalStore(
    terminalTabs.subscribe,
    terminalTabs.getTabs,
    terminalTabs.getTabs,
  );
  const active = useSyncExternalStore(
    terminalTabs.subscribe,
    terminalTabs.getActiveId,
    terminalTabs.getActiveId,
  );
  return { tabs: snapTabs, activeId: active };
}

let seq = 0;

/** newTabID returns a unique client-side tab id. */
export function newTabID(): string {
  seq += 1;
  return `tab-${Date.now().toString(36)}-${seq}`;
}

/** tabLabel renders the human label for a tab. */
export function tabLabel(tab: Pick<TerminalTab, "kind" | "target" | "label">): string {
  if (tab.label) return tab.label;
  return tab.kind === "ssh" ? `ssh ${tab.target}` : "shell";
}
