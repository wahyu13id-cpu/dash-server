import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { ApiError, api } from "../lib/api";
import type { ProfileInfo } from "../lib/types";
import { useAuth } from "../context/AuthContext";
import { useToast } from "../context/ToastContext";
import { formatDateTime } from "../lib/format";
import { Badge, Card } from "../components/Card";
import { Button } from "../components/Button";
import { Spinner } from "../components/Feedback";
import {
  IconCheck,
  IconEye,
  IconEyeOff,
  IconKey,
  IconShield,
  IconUser,
} from "../components/Icons";

/**
 * Turns an API failure into a sentence a person can act on. Unknown codes fall
 * back to the server message so nothing is ever swallowed.
 */
function explain(err: unknown, fallback: string): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "unauthorized":
        return "Kata sandi saat ini salah.";
      case "conflict":
        return "Nama pengguna itu sudah dipakai. Pilih nama lain.";
      case "invalid_username":
        return "Nama pengguna harus 3-32 karakter, diawali dan diakhiri huruf atau angka, serta hanya berisi huruf, angka, titik, strip, atau garis bawah.";
      case "weak_password":
        return "Kata sandi terlalu lemah: minimal 10 karakter dan harus berisi huruf serta angka.";
      case "rate_limited":
        return "Terlalu banyak percobaan. Coba lagi beberapa menit lagi.";
      case "network_error":
        return "Tidak dapat menghubungi server.";
      default:
        return err.message || fallback;
    }
  }
  return err instanceof Error ? err.message : fallback;
}

export function ProfilePage() {
  const { user } = useAuth();

  const [info, setInfo] = useState<ProfileInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      setInfo(await api.profile());
    } catch (err) {
      setLoadError(explain(err, "Gagal memuat data profil."));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading && !info) {
    return (
      <div className="page">
        <Spinner label="Memuat profil..." />
      </div>
    );
  }

  if (loadError || !info) {
    return (
      <div className="page">
        <Card title="Profil">
          <p className="form-error" role="alert">
            {loadError || "Data profil tidak tersedia."}
          </p>
          <div className="row-actions">
            <Button variant="secondary" onClick={() => void load()}>
              Coba lagi
            </Button>
          </div>
        </Card>
      </div>
    );
  }

  return (
    <div className="page page--profile">
      <div className="profile-grid">
        <AccountCard info={info} fallbackName={user?.username ?? ""} />

        <div className="profile-main">
          <IdentityCard info={info} onSaved={load} />
          <PasswordCard info={info} />
          <SessionCard info={info} />
        </div>
      </div>
    </div>
  );
}

/** Left column: who you are signed in as, at a glance. */
function AccountCard({ info, fallbackName }: { info: ProfileInfo; fallbackName: string }) {
  const username = info.user?.username || fallbackName;
  const displayName = info.user?.displayName || username;

  return (
    <Card className="profile-identity">
      <div className="profile-identity__top">
        <span className="avatar avatar--lg" aria-hidden="true">
          {displayName.slice(0, 1).toUpperCase()}
        </span>
        <div className="profile-identity__names">
          <strong>{displayName}</strong>
          <small>@{username}</small>
        </div>
      </div>

      <div className="profile-identity__badges">
        <Badge tone="info">{info.user?.role || "admin"}</Badge>
        {info.totpEnabled ? (
          <Badge tone="success">2FA aktif</Badge>
        ) : (
          <Badge tone="warning">2FA nonaktif</Badge>
        )}
      </div>

      <dl className="detail-list detail-list--stacked">
        <div>
          <dt>Dibuat</dt>
          <dd>{info.user?.createdAt ? formatDateTime(info.user.createdAt) : "-"}</dd>
        </div>
        <div>
          <dt>Sesi aktif</dt>
          <dd>
            {info.activeSessions} <span className="muted">perangkat</span>
          </dd>
        </div>
        <div>
          <dt>Kode pemulihan</dt>
          <dd>
            {info.totpEnabled ? (
              <>
                {info.recoveryRemaining} dari 10
                {info.recoveryRemaining <= 2 && <Badge tone="warning">segera buat ulang</Badge>}
              </>
            ) : (
              <span className="muted">belum ada</span>
            )}
          </dd>
        </div>
      </dl>

      <Link className="btn btn--ghost btn--sm profile-identity__link" to="/security">
        <IconShield width={16} height={16} />
        <span>Kelola autentikasi dua faktor</span>
      </Link>
    </Card>
  );
}

/** Display name + login name. Both need the current password to prove intent. */
function IdentityCard({ info, onSaved }: { info: ProfileInfo; onSaved: () => Promise<void> }) {
  const toast = useToast();
  const { refresh } = useAuth();

  const [displayName, setDisplayName] = useState(info.user?.displayName ?? "");
  const [username, setUsername] = useState(info.user?.username ?? "");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    setDisplayName(info.user?.displayName ?? "");
    setUsername(info.user?.username ?? "");
  }, [info]);

  const usernameNormalized = username.trim().toLowerCase();
  const usernameValid =
    usernameNormalized.length >= info.usernameMinLength &&
    usernameNormalized.length <= info.usernameMaxLength &&
    /^[a-z0-9][a-z0-9._-]*[a-z0-9]$/.test(usernameNormalized);
  const dirty =
    usernameNormalized !== (info.user?.username ?? "") ||
    displayName.trim() !== (info.user?.displayName ?? "");

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await api.updateProfile({
        currentPassword: password,
        username,
        displayName,
      });
      await refresh();
      await onSaved();
      setPassword("");
      toast.success(
        result.usernameChanged
          ? `Profil diperbarui. Nama pengguna sekarang "${result.user.username}".`
          : "Profil diperbarui.",
      );
    } catch (err) {
      setError(explain(err, "Gagal memperbarui profil."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Ubah profil" subtitle="Nama tampilan dan nama pengguna untuk masuk dashboard.">
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>Nama tampilan</span>
          <input
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
            placeholder={info.user?.username}
            autoComplete="name"
            maxLength={64}
          />
          <small className="field__hint">
            Ditampilkan di sudut kanan atas. Boleh dikosongkan untuk memakai nama pengguna.
          </small>
        </label>

        <label className="field">
          <span>Nama pengguna</span>
          <input
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            autoComplete="username"
            spellCheck={false}
            maxLength={info.usernameMaxLength}
          />
          <small className="field__hint">
            {info.usernameMinLength}-{info.usernameMaxLength} karakter: huruf kecil, angka, titik,
            strip, atau garis bawah. Dipakai untuk login berikutnya; sesi yang sedang aktif tetap
            berjalan.
          </small>
        </label>

        <label className="field">
          <span>Kata sandi saat ini</span>
          <input
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="current-password"
            required
          />
          <small className="field__hint">
            Wajib diisi sebagai konfirmasi, meski hanya mengubah nama tampilan.
          </small>
        </label>

        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}

        <div className="row-actions">
          <Button
            type="submit"
            variant="primary"
            loading={busy}
            disabled={!password || !dirty || !usernameValid}
          >
            Simpan profil
          </Button>
          {!dirty && <span className="muted">Belum ada perubahan.</span>}
        </div>
      </form>
    </Card>
  );
}

/** Password change. Rides on the same endpoint so it can be combined with a rename. */
function PasswordCard({ info }: { info: ProfileInfo }) {
  const toast = useToast();

  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const hasLetter = /\p{L}/u.test(next);
  const hasDigit = /\p{Nd}/u.test(next);
  const longEnough = next.length >= info.passwordMinLength;
  const strong = hasLetter && hasDigit && longEnough;
  const matches = confirm === "" || next === confirm;

  const submit = async () => {
    if (next !== confirm) {
      setError("Konfirmasi kata sandi tidak cocok.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.updateProfile({ currentPassword: current, newPassword: next });
      setCurrent("");
      setNext("");
      setConfirm("");
      toast.success("Kata sandi diperbarui. Sesi lain telah diakhiri.");
    } catch (err) {
      setError(explain(err, "Gagal mengubah kata sandi."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title="Ubah kata sandi"
      subtitle="Setelah disimpan, semua sesi lain diakhiri dan hanya sesi ini yang dipertahankan."
    >
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>Kata sandi saat ini</span>
          <input
            type="password"
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
            autoComplete="current-password"
          />
        </label>

        <label className="field">
          <span>Kata sandi baru</span>
          <span className="field__with-action">
            <input
              type={show ? "text" : "password"}
              value={next}
              onChange={(event) => setNext(event.target.value)}
              autoComplete="new-password"
            />
            <button
              type="button"
              className="field__action"
              onClick={() => setShow((value) => !value)}
              aria-label={show ? "Sembunyikan kata sandi" : "Tampilkan kata sandi"}
              title={show ? "Sembunyikan" : "Tampilkan"}
            >
              {show ? <IconEyeOff width={16} height={16} /> : <IconEye width={16} height={16} />}
            </button>
          </span>
        </label>

        <ul className="pwd-rules">
          <Rule ok={longEnough} label={`Minimal ${info.passwordMinLength} karakter`} />
          <Rule ok={hasLetter} label="Mengandung huruf" />
          <Rule ok={hasDigit} label="Mengandung angka" />
        </ul>

        <label className="field">
          <span>Konfirmasi kata sandi baru</span>
          <input
            type={show ? "text" : "password"}
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
            autoComplete="new-password"
            className={matches ? undefined : "input--invalid"}
          />
          {!matches && (
            <small className="field__hint field__hint--error">Kata sandi belum sama.</small>
          )}
        </label>

        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}

        <div className="row-actions">
          <Button
            type="submit"
            variant="primary"
            icon={<IconKey width={16} height={16} />}
            loading={busy}
            disabled={!current || !strong || !confirm || !matches}
          >
            Simpan kata sandi
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Rule({ ok, label }: { ok: boolean; label: string }) {
  return (
    <li className={`pwd-rule ${ok ? "pwd-rule--ok" : ""}`}>
      <IconCheck width={13} height={13} />
      <span>{label}</span>
    </li>
  );
}

/** Where this browser is signed in from, for a quick sanity check. */
function SessionCard({ info }: { info: ProfileInfo }) {
  const { user } = useAuth();

  const client = useMemo(() => describeClient(info.sessionUserAgent ?? ""), [info.sessionUserAgent]);
  const ttlHours = Math.round(info.sessionTtlSeconds / 3600);

  return (
    <Card title="Sesi ini" subtitle="Detail koneksi tempat Anda sedang masuk.">
      <dl className="detail-list detail-list--stacked">
        <div>
          <dt>Akun</dt>
          <dd>
            <IconUser width={15} height={15} />
            {user?.displayName || info.user?.username}
          </dd>
        </div>
        <div>
          <dt>Mulai</dt>
          <dd>{info.sessionCreatedAt ? formatDateTime(info.sessionCreatedAt) : "-"}</dd>
        </div>
        <div>
          <dt>Berakhir</dt>
          <dd>
            {info.sessionExpiresAt ? formatDateTime(info.sessionExpiresAt) : "-"}
            {ttlHours > 0 && <span className="muted">({ttlHours} jam sejak login)</span>}
          </dd>
        </div>
        <div>
          <dt>Alamat</dt>
          <dd>{info.sessionIp || "-"}</dd>
        </div>
        <div>
          <dt>Perangkat</dt>
          <dd title={info.sessionUserAgent}>{client}</dd>
        </div>
      </dl>
    </Card>
  );
}

/** Short, human summary of a user-agent string ("Chrome di Linux"). */
function describeClient(userAgent: string): string {
  if (!userAgent) return "Tidak diketahui";

  const browser =
    /Firefox\/(\d+)/.exec(userAgent)?.[1] ? "Firefox" :
    /Edg\/(\d+)/.exec(userAgent)?.[1] ? "Edge" :
    /OPR\/(\d+)/.exec(userAgent)?.[1] ? "Opera" :
    /Chrome\/(\d+)/.exec(userAgent) ? "Chrome" :
    /Version\/[\d.]+.*Safari/.test(userAgent) ? "Safari" :
    /curl\//.test(userAgent) ? "curl" :
    "Peramban lain";

  const os =
    /Windows NT/.test(userAgent) ? "Windows" :
    /Android/.test(userAgent) ? "Android" :
    /iPhone|iPad|iOS/.test(userAgent) ? "iOS" :
    /Mac OS X/.test(userAgent) ? "macOS" :
    /Linux/.test(userAgent) ? "Linux" :
    "";

  return os ? `${browser} di ${os}` : browser;
}
