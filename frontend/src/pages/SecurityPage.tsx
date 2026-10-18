import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import type { TotpSetup } from "../lib/types";
import { useAuth } from "../context/AuthContext";
import { useToast } from "../context/ToastContext";
import { Button } from "../components/Button";
import { Card, Badge } from "../components/Card";
import { Modal } from "../components/Modal";
import { IconCopy, IconShield } from "../components/Icons";
import { Spinner } from "../components/Feedback";

type Phase = "loading" | "configure" | "recovery" | "manage";

export function SecurityPage() {
  const toast = useToast();
  const { user } = useAuth();

  const [phase, setPhase] = useState<Phase>("loading");
  const [enabled, setEnabled] = useState(false);
  const [remaining, setRemaining] = useState(0);
  const [issuer, setIssuer] = useState("");
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const [disableOpen, setDisableOpen] = useState(false);
  const [regenerateOpen, setRegenerateOpen] = useState(false);

  const load = useCallback(async () => {
    try {
      const status = await api.totpStatus();
      setEnabled(status.enabled);
      setRemaining(status.recoveryRemaining);
      setIssuer(status.issuer);
      setPhase(status.enabled ? "manage" : "configure");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal memuat status 2FA.");
      setPhase("configure");
    }
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  const beginSetup = async () => {
    setBusy(true);
    setError("");
    try {
      setSetup(await api.totpSetup());
      setCode("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal memulai penyiapan 2FA.");
    } finally {
      setBusy(false);
    }
  };

  const activate = async () => {
    setBusy(true);
    setError("");
    try {
      const { recoveryCodes: codes } = await api.totpActivate(code.trim());
      setRecoveryCodes(codes);
      setSetup(null);
      setCode("");
      setPhase("recovery");
      toast.success("Autentikasi dua faktor diaktifkan.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Kode tidak valid.");
      setCode("");
    } finally {
      setBusy(false);
    }
  };

  const finishRecovery = () => {
    setRecoveryCodes([]);
    void load();
  };

  if (phase === "loading") {
    return (
      <div className="page">
        <Spinner label="Memuat pengaturan keamanan..." />
      </div>
    );
  }

  return (
    <div className="page">
      <Card
        title="Autentikasi dua faktor"
        actions={
          enabled ? (
            <Badge tone="success">aktif</Badge>
          ) : (
            <Badge tone="neutral">nonaktif</Badge>
          )
        }
      >
        <div className="security-intro">
          <span className="security-intro__icon" aria-hidden="true">
            <IconShield width={22} height={22} />
          </span>
          <div>
            <p>
              Setelah aktif, setiap login memerlukan kode 6 digit dari aplikasi
              autentikator (Google Authenticator, Authy, 1Password) sebagai faktor
              kedua di samping kata sandi.
            </p>
            <p className="muted">
              {enabled
                ? "Simpan kode pemulihan Anda di tempat aman: kode itu satu-satunya cara masuk bila ponsel hilang."
                : "Anda akan memindai kode QR, lalu memverifikasi dengan satu kode."}
            </p>
          </div>
        </div>

        {phase === "configure" && !setup && (
          <div className="row-actions">
            <Button variant="primary" onClick={beginSetup} loading={busy}>
              Aktifkan autentikasi dua faktor
            </Button>
          </div>
        )}

        {phase === "configure" && setup && (
          <div className="totp-setup">
            <div className="totp-setup__qr">
              <img src={setup.qrPng} alt="Kode QR untuk aplikasi autentikator" width={200} height={200} />
            </div>
            <div className="totp-setup__steps">
              <ol className="totp-steps">
                <li>Buka aplikasi autentikator dan pilih “Pindai kode QR”.</li>
                <li>
                  Tidak bisa memindai? Masukkan kunci ini secara manual:
                  <CopyField value={setup.secret} />
                </li>
                <li>Masukkan kode 6 digit yang muncul untuk menyelesaikan.</li>
              </ol>
              <div className="totp-verify">
                <input
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
                  placeholder="000000"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  className="field__code"
                  maxLength={6}
                  aria-label="Kode verifikasi"
                />
                <Button
                  variant="primary"
                  onClick={activate}
                  loading={busy}
                  disabled={code.length !== 6}
                >
                  Verifikasi dan aktifkan
                </Button>
              </div>
              {error && (
                <p className="form-error" role="alert">
                  {error}
                </p>
              )}
              <button type="button" className="link-btn" onClick={() => setSetup(null)}>
                Batal
              </button>
            </div>
          </div>
        )}

        {phase === "recovery" && (
          <div className="totp-recovery">
            <p>
              <strong>Simpan kode pemulihan ini sekarang.</strong> Setiap kode hanya
              dapat dipakai sekali dan tidak akan ditampilkan lagi.
            </p>
            <RecoveryGrid codes={recoveryCodes} />
            <div className="row-actions">
              <Button variant="primary" onClick={finishRecovery}>
                Saya sudah menyimpannya
              </Button>
            </div>
          </div>
        )}

        {phase === "manage" && (
          <div className="security-manage">
            <dl className="detail-list">
              <div>
                <dt>Penerbit</dt>
                <dd>{issuer || "Server Dashboard"}</dd>
              </div>
              <div>
                <dt>Akun</dt>
                <dd>{user?.username}</dd>
              </div>
              <div>
                <dt>Kode pemulihan tersisa</dt>
                <dd>
                  {remaining} dari 10{" "}
                  {remaining <= 2 && <Badge tone="warning">segera buat ulang</Badge>}
                </dd>
              </div>
            </dl>
            <div className="row-actions">
              <Button variant="ghost" onClick={() => setRegenerateOpen(true)}>
                Buat ulang kode pemulihan
              </Button>
              <Button variant="danger" onClick={() => setDisableOpen(true)}>
                Nonaktifkan
              </Button>
            </div>
          </div>
        )}
      </Card>

      <RecoveryCodesModal
        open={regenerateOpen}
        onClose={() => setRegenerateOpen(false)}
        onDone={(codes) => {
          setRegenerateOpen(false);
          setRecoveryCodes(codes);
          setPhase("recovery");
        }}
      />

      <DisableTOTPModal
        open={disableOpen}
        onClose={() => setDisableOpen(false)}
        onDone={() => {
          setDisableOpen(false);
          setEnabled(false);
          setRemaining(0);
          setPhase("configure");
          setSetup(null);
          toast.success("Autentikasi dua faktor dinonaktifkan.");
        }}
      />
    </div>
  );
}

function CopyField({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <span className="copy-field">
      <code>{value}</code>
      <button type="button" className="btn btn--ghost btn--sm" onClick={copy}>
        <IconCopy width={15} height={15} />
        {copied ? "Tersalin" : "Salin"}
      </button>
    </span>
  );
}

function RecoveryGrid({ codes }: { codes: string[] }) {
  const [copied, setCopied] = useState(false);

  const copyAll = async () => {
    try {
      await navigator.clipboard.writeText(codes.join("\n"));
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  const download = () => {
    const blob = new Blob([codes.join("\n") + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "kode-pemulihan-dashboard.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="recovery-block">
      <ol className="recovery-grid">
        {codes.map((c) => (
          <li key={c}>
            <code>{c}</code>
          </li>
        ))}
      </ol>
      <div className="row-actions">
        <Button variant="ghost" onClick={copyAll}>
          <IconCopy width={15} height={15} /> {copied ? "Tersalin" : "Salin semua"}
        </Button>
        <Button variant="ghost" onClick={download}>
          Unduh .txt
        </Button>
      </div>
    </div>
  );
}

function CodeAndPasswordFields({
  password,
  setPassword,
  code,
  setCode,
}: {
  password: string;
  setPassword: (v: string) => void;
  code: string;
  setCode: (v: string) => void;
}) {
  return (
    <div className="form-stack">
      <label className="field">
        <span>Kata sandi saat ini</span>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
        />
      </label>
      <label className="field">
        <span>Kode autentikator atau kode pemulihan</span>
        <input
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="000000 atau XXXXX-XXXXX"
          autoComplete="one-time-code"
          spellCheck={false}
        />
      </label>
    </div>
  );
}

function DisableTOTPModal({
  open,
  onClose,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await api.totpDisable(password, code.trim());
      setPassword("");
      setCode("");
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal menonaktifkan 2FA.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title="Nonaktifkan autentikasi dua faktor"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button
            variant="danger"
            onClick={submit}
            loading={busy}
            disabled={!password || code.trim().length < 6}
          >
            Nonaktifkan
          </Button>
        </>
      }
    >
      <CodeAndPasswordFields password={password} setPassword={setPassword} code={code} setCode={setCode} />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}

function RecoveryCodesModal({
  open,
  onClose,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  onDone: (codes: string[]) => void;
}) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const { recoveryCodes } = await api.totpRegenerateRecovery(password, code.trim());
      setPassword("");
      setCode("");
      onDone(recoveryCodes);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal membuat ulang kode pemulihan.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title="Buat ulang kode pemulihan"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button
            variant="primary"
            onClick={submit}
            loading={busy}
            disabled={!password || code.trim().length < 6}
          >
            Buat ulang
          </Button>
        </>
      }
    >
      <p className="muted">
        Kode pemulihan lama akan langsung tidak berlaku. Konfirmasi dengan kata sandi
        dan kode autentikator Anda.
      </p>
      <CodeAndPasswordFields password={password} setPassword={setPassword} code={code} setCode={setCode} />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}
