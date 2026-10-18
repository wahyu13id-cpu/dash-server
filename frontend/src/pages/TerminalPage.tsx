import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { api } from "../lib/api";
import { Badge } from "../components/Card";
import { Button } from "../components/Button";
import { EmptyState, Spinner } from "../components/Feedback";
import { IconClose, IconPlus, IconPlug, IconTerminal } from "../components/Icons";
import { TerminalPane, type PaneStatus } from "../components/TerminalPane";
import {
  newShellTab,
  newTabID,
  reconcileTabs,
  tabLabel,
  tabsNeedRestore,
  terminalTabs,
  useTerminalTabs,
  type TerminalTab,
} from "../lib/terminalTabs";
import type { SshHost } from "../lib/types";

const STATUS_TONE: Record<PaneStatus, "success" | "warning" | "danger" | "neutral"> = {
  open: "success",
  connecting: "warning",
  error: "danger",
  closed: "neutral",
};

const STATUS_LABEL: Record<PaneStatus, string> = {
  open: "terhubung",
  connecting: "menghubungkan",
  error: "gagal",
  closed: "terputus (sesi berakhir)",
};

export function TerminalPage() {
  const { tabs, activeId } = useTerminalTabs();
  const [params, setParams] = useSearchParams();

  const [statuses, setStatuses] = useState<Record<string, PaneStatus>>({});
  const [menuOpen, setMenuOpen] = useState(false);
  const [hosts, setHosts] = useState<SshHost[] | null>(null);
  const [hostsError, setHostsError] = useState("");
  const [confirmCloseAll, setConfirmCloseAll] = useState(false);
  // A reload hands us the previous tab list with server session ids in it. Those
  // must be validated before any pane attaches, otherwise the page would open a
  // new PTY for every dead id and eat into the session cap.
  const [restoring, setRestoring] = useState(() => tabsNeedRestore());
  const menuRef = useRef<HTMLDivElement>(null);

  const requestedHost = params.get("host") ?? "";
  // "Buka di Terminal" from the file manager arrives as ?dir=<path>: the shell
  // is created straight in that directory.
  const requestedDir = params.get("dir") ?? "";

  useEffect(() => {
    if (!restoring) return;
    let cancelled = false;
    void (async () => {
      let live: string[] = [];
      try {
        const state = await api.listTerminalSessions();
        live = (state.sessions ?? []).map((session) => session.id);
      } catch {
        // The list is the only way to know what still exists, so fall back to
        // starting over rather than attaching to sessions that may be gone.
        live = [];
      }
      if (cancelled) return;
      reconcileTabs(live);
      setRestoring(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [restoring]);

  const addTab = useCallback((kind: "shell" | "ssh", target: string, dir = "") => {
    const existing =
      kind === "ssh" ? tabs.find((tab) => tab.kind === "ssh" && tab.target === target) : undefined;
    if (existing) {
      terminalTabs.setActive(existing.id);
      return existing.id;
    }
    const tab: TerminalTab = {
      id: newTabID(),
      kind,
      target,
      dir,
      sessionId: "",
      label: kind === "ssh" ? `ssh ${target}` : dir || "shell",
    };
    terminalTabs.add(tab);
    return tab.id;
  }, [tabs]);

  // Keep at least one tab so the page always opens on a ready root shell.
  useEffect(() => {
    if (requestedHost) {
      addTab("ssh", requestedHost);
      const next = new URLSearchParams(params);
      next.delete("host");
      setParams(next, { replace: true });
      return;
    }
    if (requestedDir) {
      addTab("shell", "", requestedDir);
      const next = new URLSearchParams(params);
      next.delete("dir");
      setParams(next, { replace: true });
      return;
    }
    if (terminalTabs.getTabs().length === 0) {
      addTab("shell", "");
    }
  }, [requestedHost, requestedDir, params, setParams, addTab]);

  const loadHosts = useCallback(async () => {
    try {
      const state = await api.sshState();
      setHosts(state.hosts ?? []);
      setHostsError("");
    } catch (err) {
      setHosts([]);
      setHostsError(err instanceof Error ? err.message : "Tidak dapat memuat daftar host.");
    }
  }, []);

  useEffect(() => {
    if (!menuOpen || hosts !== null) return;
    void loadHosts();
  }, [menuOpen, hosts, loadHosts]);

  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, []);

  const handleStatus = useCallback((tabId: string, status: PaneStatus) => {
    setStatuses((prev) => (prev[tabId] === status ? prev : { ...prev, [tabId]: status }));
  }, []);

  const closeTab = useCallback((tab: TerminalTab) => {
    if (tab.sessionId) {
      void api.deleteTerminalSession(tab.sessionId).catch(() => undefined);
    }
    terminalTabs.remove(tab.id);
    // Closing the last tab immediately opens a fresh local shell.
    if (terminalTabs.getTabs().length === 0) {
      terminalTabs.add(newShellTab());
    }
  }, []);

  const closeAll = useCallback(() => {
    const current = terminalTabs.getTabs();
    for (const tab of current) {
      if (tab.sessionId) {
        void api.deleteTerminalSession(tab.sessionId).catch(() => undefined);
      }
    }
    terminalTabs.clear();
    terminalTabs.add(newShellTab());
    setStatuses({});
    setConfirmCloseAll(false);
  }, []);

  const activeStatus = useMemo(
    () => (activeId ? statuses[activeId] ?? "connecting" : "connecting"),
    [activeId, statuses],
  );

  const favouriteHosts = useMemo(
    () => (hosts ?? []).filter((h) => h.favorite),
    [hosts],
  );
  const otherHosts = useMemo(() => (hosts ?? []).filter((h) => !h.favorite), [hosts]);

  return (
    <div className="page page--terminal">
      <div className="terminal-toolbar">
        <div className="term-tabs" role="tablist" aria-label="Sesi terminal">
          {tabs.map((tab) => {
            const status = statuses[tab.id] ?? "connecting";
            return (
              <div
                key={tab.id}
                className={`term-tab ${tab.id === activeId ? "term-tab--active" : ""}`}
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={tab.id === activeId}
                  className="term-tab__label"
                  onClick={() => terminalTabs.setActive(tab.id)}
                >
                  <span className={`term-tab__dot term-tab__dot--${status}`} aria-hidden="true" />
                  {tab.kind === "ssh" ? <IconPlug width={14} height={14} /> : <IconTerminal width={14} height={14} />}
                  <span className="term-tab__text">{tabLabel(tab)}</span>
                </button>
                <button
                  type="button"
                  className="term-tab__close"
                  aria-label={`Tutup ${tabLabel(tab)}`}
                  onClick={() => closeTab(tab)}
                >
                  <IconClose width={13} height={13} />
                </button>
              </div>
            );
          })}

          <div className="term-tabs__add" ref={menuRef}>
            <button
              type="button"
              className="term-tab-add"
              aria-label="Sesi baru"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              onClick={() => setMenuOpen((open) => !open)}
            >
              <IconPlus width={16} height={16} />
            </button>
            {menuOpen && (
              <div className="term-menu" role="menu">
                <button
                  type="button"
                  role="menuitem"
                  className="term-menu__item"
                  onClick={() => {
                    addTab("shell", "");
                    setMenuOpen(false);
                  }}
                >
                  <IconTerminal width={15} height={15} />
                  <span>Shell root lokal</span>
                </button>
                <div className="term-menu__sep" />
                <div className="term-menu__head">Sesi SSH</div>
                {hosts === null && <div className="term-menu__note">Memuat host...</div>}
                {hostsError && <div className="term-menu__note term-menu__note--error">{hostsError}</div>}
                {hosts !== null && hosts.length === 0 && !hostsError && (
                  <div className="term-menu__note">
                    Belum ada host. Tambahkan di halaman <strong>Sesi SSH</strong>.
                  </div>
                )}
                {[...favouriteHosts, ...otherHosts].slice(0, 30).map((host) => (
                  <button
                    key={host.alias}
                    type="button"
                    role="menuitem"
                    className="term-menu__item"
                    onClick={() => {
                      addTab("ssh", host.alias);
                      setMenuOpen(false);
                    }}
                  >
                    <IconPlug width={15} height={15} />
                    <span className="term-menu__alias">{host.alias}</span>
                    <span className="term-menu__sub">
                      {host.user ? `${host.user}@` : ""}
                      {host.hostName || host.alias}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="terminal-toolbar__group">
          <Badge tone={STATUS_TONE[activeStatus]}>{STATUS_LABEL[activeStatus]}</Badge>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setConfirmCloseAll(true)}
            disabled={tabs.length === 0}
          >
            Tutup semua sesi
          </Button>
        </div>
      </div>

      {confirmCloseAll && (
        <div className="term-confirm" role="alertdialog" aria-label="Tutup semua sesi">
          <span>Menutup semua sesi akan mengakhiri setiap proses yang berjalan di dalamnya.</span>
          <div className="term-confirm__actions">
            <Button variant="ghost" size="sm" onClick={() => setConfirmCloseAll(false)}>
              Batal
            </Button>
            <Button variant="danger" size="sm" onClick={closeAll}>
              Tutup semua
            </Button>
          </div>
        </div>
      )}

      <div className="terminal-stage">
        {restoring && (
          <div className="terminal-stage__placeholder">
            <Spinner label="Menyambungkan kembali sesi sebelumnya..." />
          </div>
        )}
        {!restoring && tabs.length === 0 && (
          <div className="terminal-stage__placeholder">
            <EmptyState
              icon={<IconTerminal width={26} height={26} />}
              title="Belum ada sesi"
              description="Buka shell root lokal atau pilih host SSH dari tombol tambah."
              action={
                <Button variant="primary" size="sm" onClick={() => addTab("shell", "")}>
                  Shell baru
                </Button>
              }
            />
          </div>
        )}
        {!restoring &&
          tabs.map((tab) => (
            <TerminalPane
              key={tab.id}
              tab={tab}
              visible={tab.id === activeId}
              onStatus={handleStatus}
            />
          ))}
      </div>
    </div>
  );
}
