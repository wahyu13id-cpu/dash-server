import { useCallback, useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import type { OpsAvailability } from "../lib/types";
import { useAuth } from "../context/AuthContext";
import { useOps } from "../context/OpsContext";
import { useToast } from "../context/ToastContext";
import { Button } from "./Button";
import { Badge } from "./Card";
import { ConfirmDialog, Modal } from "./Modal";
import { FloatingTerminalLayer } from "./FloatingTerminalLayer";
import {
  IconActivity,
  IconBox,
  IconCog,
  IconDashboard,
  IconFolder,
  IconKey,
  IconLogout,
  IconMenu,
  IconPlug,
  IconScroll,
  IconServer,
  IconShield,
  IconTerminal,
  IconUser,
} from "./Icons";

type NavIcon = typeof IconDashboard;

interface NavItem {
  to: string;
  label: string;
  icon: NavIcon;
  end: boolean;
  /**
   * Hidden when the host does not support the feature, so a host without
   * systemd never shows a services entry that cannot work.
   */
  available?: (a: OpsAvailability) => boolean;
}

const NAV_GROUPS: { label?: string; items: NavItem[] }[] = [
  {
    items: [
      { to: "/", label: "Dashboard", icon: IconDashboard, end: true },
      { to: "/terminal", label: "Terminal", icon: IconTerminal, end: false },
      { to: "/ssh", label: "Sesi SSH", icon: IconPlug, end: false },
      { to: "/files", label: "Files", icon: IconFolder, end: false },
    ],
  },
  {
    label: "Sistem",
    items: [
      { to: "/services", label: "Layanan", icon: IconCog, end: false, available: (a) => a.systemd },
      { to: "/processes", label: "Proses", icon: IconActivity, end: false, available: (a) => a.processes },
      { to: "/logs", label: "Log", icon: IconScroll, end: false, available: (a) => a.journal || !!a.logDir },
      { to: "/packages", label: "Pembaruan", icon: IconBox, end: false, available: (a) => !!a.packageManager },
    ],
  },
  {
    items: [{ to: "/security", label: "Keamanan", icon: IconShield, end: false }],
  },
];

const PAGE_TITLES: Record<string, string> = {
  "/": "Dashboard",
  "/terminal": "Terminal",
  "/ssh": "Sesi SSH",
  "/files": "File Manager",
  "/services": "Layanan Sistem",
  "/processes": "Proses",
  "/logs": "Log Sistem",
  "/packages": "Pembaruan Paket",
  "/security": "Keamanan",
  "/profile": "Profil saya",
};

/** Keep in sync with the 900px breakpoint in styles/global.css. */
const MOBILE_NAV_QUERY = "(max-width: 900px)";

/**
 * Tracks the sidebar breakpoint. Below it the sidebar becomes an off-canvas
 * drawer (opened by the topbar button), above it the sidebar is always in the
 * layout and the same button collapses it instead.
 */
function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(
    () => typeof window !== "undefined" && window.matchMedia(query).matches,
  );

  useEffect(() => {
    const list = window.matchMedia(query);
    const onChange = () => setMatches(list.matches);
    onChange();
    list.addEventListener("change", onChange);
    return () => list.removeEventListener("change", onChange);
  }, [query]);

  return matches;
}

export function Layout() {
  const { user, logout } = useAuth();
  const { availability } = useOps();
  const toast = useToast();
  const location = useLocation();
  const navigate = useNavigate();

  const [collapsed, setCollapsed] = useState<boolean>(
    () => localStorage.getItem("dash.sidebar.collapsed") === "1",
  );
  const [mobileOpen, setMobileOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [healthOk, setHealthOk] = useState<boolean | null>(null);
  const [pwdOpen, setPwdOpen] = useState(false);
  const [logoutOpen, setLogoutOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    localStorage.setItem("dash.sidebar.collapsed", collapsed ? "1" : "0");
  }, [collapsed]);

  useEffect(() => {
    setMobileOpen(false);
    setMenuOpen(false);
  }, [location.pathname]);

  useEffect(() => {
    let active = true;
    const check = async () => {
      try {
        await api.health();
        if (active) setHealthOk(true);
      } catch {
        if (active) setHealthOk(false);
      }
    };
    void check();
    const timer = window.setInterval(check, 30_000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, []);

  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, []);

  const title = PAGE_TITLES[location.pathname] ?? "Dashboard";

  const isMobileNav = useMediaQuery(MOBILE_NAV_QUERY);

  // One button, two behaviours: on a narrow screen it opens/closes the
  // off-canvas drawer, on desktop it collapses/expands the sidebar rail.
  // It used to only touch the mobile state, so on desktop it did nothing.
  const toggleSidebar = () => {
    if (isMobileNav) {
      setMobileOpen((open) => !open);
    } else {
      setCollapsed((value) => !value);
    }
  };

  const sidebarVisible = isMobileNav ? mobileOpen : !collapsed;
  const sidebarToggleLabel = isMobileNav
    ? mobileOpen
      ? "Tutup navigasi"
      : "Buka navigasi"
    : collapsed
      ? "Perluas sidebar"
      : "Ciutkan sidebar";

  const handleLogout = useCallback(async () => {
    await logout();
    toast.success("Anda telah keluar.");
  }, [logout, toast]);

  return (
    <div className={`app-shell ${collapsed ? "app-shell--collapsed" : ""}`}>
      <a className="skip-link" href="#main-content">
        Lewati ke konten
      </a>

      <aside id="app-sidebar" className={`sidebar ${mobileOpen ? "sidebar--open" : ""}`}>
        <div className="sidebar__brand">
          <span className="brand__mark" aria-hidden="true">
            <IconServer width={20} height={20} />
          </span>
          {!collapsed && (
            <span className="brand__text">
              <strong>Server Dashboard</strong>
              <small>control panel</small>
            </span>
          )}
        </div>

        <nav className="sidebar__nav" aria-label="Navigasi utama">
          {NAV_GROUPS.map((group, index) => {
            const items = group.items.filter(
              (item) => !item.available || !availability || item.available(availability),
            );
            if (items.length === 0) return null;
            return (
              <div className="nav-group" key={group.label ?? `group-${index}`}>
                {group.label &&
                  (collapsed ? (
                    <div className="nav-group__divider" aria-hidden="true" />
                  ) : (
                    <div className="nav-group__label">{group.label}</div>
                  ))}
                {items.map((item) => {
                  const Icon = item.icon;
                  return (
                    <NavLink
                      key={item.to}
                      to={item.to}
                      end={item.end}
                      className={({ isActive }) => `nav-item ${isActive ? "nav-item--active" : ""}`}
                      title={collapsed ? item.label : undefined}
                    >
                      <Icon width={18} height={18} />
                      {!collapsed && <span>{item.label}</span>}
                    </NavLink>
                  );
                })}
              </div>
            );
          })}
        </nav>

        <div className="sidebar__footer">
          {!collapsed && (
            <div className="sidebar__status">
              <span className={`status-dot ${healthOk ? "status-dot--ok" : healthOk === false ? "status-dot--down" : ""}`} />
              <span>{healthOk === null ? "Memeriksa server..." : healthOk ? "Server online" : "Server tidak terjangkau"}</span>
            </div>
          )}
          <button
            className="btn btn--ghost btn--sm sidebar__collapse"
            onClick={() => setCollapsed((c) => !c)}
            type="button"
            aria-label={collapsed ? "Perluas sidebar" : "Ciutkan sidebar"}
          >
            {collapsed ? <IconChevronRightGlyph /> : <IconChevronLeftGlyph />}
            {!collapsed && <span>Ciutkan</span>}
          </button>
        </div>
      </aside>

      {mobileOpen && <div className="sidebar-scrim" onClick={() => setMobileOpen(false)} aria-hidden="true" />}

      <div className="main">
        <header className="topbar">
          <button
            className="icon-btn icon-btn--ghost topbar__menu"
            onClick={toggleSidebar}
            aria-label={sidebarToggleLabel}
            aria-expanded={sidebarVisible}
            aria-controls="app-sidebar"
            type="button"
          >
            <IconMenu />
          </button>
          <h1 className="topbar__title">{title}</h1>

          <div className="topbar__spacer" />

          <Badge tone={healthOk ? "success" : healthOk === false ? "danger" : "neutral"}>
            {healthOk === null ? "memeriksa" : healthOk ? "online" : "offline"}
          </Badge>

          <div className="user-menu" ref={menuRef}>
            <button className="user-menu__trigger" onClick={() => setMenuOpen((o) => !o)} type="button" aria-haspopup="menu" aria-expanded={menuOpen}>
              <span className="avatar" aria-hidden="true">
                {(user?.displayName || user?.username || "?").slice(0, 1).toUpperCase()}
              </span>
              <span className="user-menu__name">{user?.displayName || user?.username}</span>
            </button>
            {menuOpen && (
              <div className="user-menu__dropdown" role="menu">
                <div className="user-menu__header">
                  <strong>{user?.displayName || user?.username}</strong>
                  <small>@{user?.username}</small>
                </div>
                <button
                  className="menu-item"
                  role="menuitem"
                  onClick={() => {
                    setMenuOpen(false);
                    navigate("/profile");
                  }}
                  type="button"
                >
                  <IconUser width={16} height={16} />
                  <span>Profil saya</span>
                </button>
                <button className="menu-item" role="menuitem" onClick={() => setPwdOpen(true)} type="button">
                  <IconKey width={16} height={16} />
                  <span>Ubah kata sandi</span>
                </button>
                <button className="menu-item menu-item--danger" role="menuitem" onClick={() => setLogoutOpen(true)} type="button">
                  <IconLogout width={16} height={16} />
                  <span>Keluar</span>
                </button>
              </div>
            )}
          </div>
        </header>

        <main className="content" id="main-content">
          <Outlet />
        </main>

        <footer className="app-footer">
          <span
            className="app-footer__note"
            title="Akses hanya untuk pengguna terautentikasi. Jalankan di jaringan tepercaya atau di belakang HTTPS/VPN."
          >
            Akses hanya untuk pengguna terautentikasi. Jalankan di jaringan tepercaya atau di belakang HTTPS/VPN.
          </span>
        </footer>
      </div>

      <ChangePasswordModal open={pwdOpen} onClose={() => setPwdOpen(false)} />

      {/* Terminal windows opened from anywhere (file manager, SSH) float above
          the page and keep running while the dashboard is navigated. */}
      <FloatingTerminalLayer />

      <ConfirmDialog
        open={logoutOpen}
        title="Keluar dari dashboard?"
        message="Sesi terminal yang sedang berjalan akan tetap aktif sampai ditutup atau timeout."
        confirmLabel="Keluar"
        destructive
        onCancel={() => setLogoutOpen(false)}
        onConfirm={async () => {
          setLogoutOpen(false);
          await handleLogout();
        }}
      />
    </div>
  );
}

function IconChevronLeftGlyph() {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14.5 6.5L9 12l5.5 5.5" />
    </svg>
  );
}

function IconChevronRightGlyph() {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M9.5 6.5L15 12l-5.5 5.5" />
    </svg>
  );
}

function ChangePasswordModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const reset = () => {
    setCurrent("");
    setNext("");
    setConfirm("");
    setError("");
  };

  const submit = async () => {
    if (next !== confirm) {
      setError("Konfirmasi kata sandi tidak cocok.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.changePassword(current, next);
      toast.success("Kata sandi diperbarui. Sesi lain telah diakhiri.");
      reset();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal mengubah kata sandi.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title="Ubah kata sandi"
      onClose={() => {
        reset();
        onClose();
      }}
      footer={
        <>
          <Button variant="ghost" onClick={() => { reset(); onClose(); }} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!current || !next}>
            Simpan
          </Button>
        </>
      }
    >
      <div className="form-stack">
        <label className="field">
          <span>Kata sandi saat ini</span>
          <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />
        </label>
        <label className="field">
          <span>Kata sandi baru</span>
          <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
          <small className="field__hint">Minimal 10 karakter, mengandung huruf dan angka.</small>
        </label>
        <label className="field">
          <span>Konfirmasi kata sandi baru</span>
          <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" />
        </label>
        {error && <p className="form-error" role="alert">{error}</p>}
      </div>
    </Modal>
  );
}
