import { useCallback, useEffect, useMemo, useState } from "react";

import { api } from "../../lib/api";
import { Button } from "../Button";
import { Badge } from "../Card";
import { ConfirmDialog } from "../Modal";
import { useToast } from "../../context/ToastContext";
import {
  IconAlert,
  IconArrowUpRight,
  IconCheck,
  IconCopy,
  IconEdit,
  IconFingerprint,
  IconLock,
  IconPlay,
  IconRefresh,
  IconSearch,
  IconStar,
  IconTrash,
} from "../Icons";
import { formatRelative } from "../../lib/format";
import { hostEndpoint } from "../../lib/ssh";
import type {
  SshEffective,
  SshHost,
  SshHostKeyFP,
  SshHostKeyInfo,
  SshKey,
  SshSecretKind,
  SshTestResult,
} from "../../lib/types";

interface SshDetailProps {
  host: SshHost;
  keys: SshKey[];
  onEdit: (host: SshHost) => void;
  onDelete: (host: SshHost) => void;
  onOpenTerminal: (alias: string) => void;
  onToggleFavorite: (host: SshHost) => void;
  onChanged: () => void;
}

const TEST_TONE: Record<string, "success" | "warning" | "danger" | "neutral"> = {
  ok: "success",
  auth_required: "warning",
  host_key_unknown: "warning",
  unreachable: "danger",
  dns_error: "danger",
  error: "danger",
};

export function SshDetail({
  host,
  keys,
  onEdit,
  onDelete,
  onOpenTerminal,
  onToggleFavorite,
  onChanged,
}: SshDetailProps) {
  const toast = useToast();
  const [testResult, setTestResult] = useState<SshTestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [hostKey, setHostKey] = useState<SshHostKeyInfo | null>(null);
  const [scanned, setScanned] = useState<SshHostKeyFP[] | null>(null);
  const [hostKeyBusy, setHostKeyBusy] = useState("");
  const [effective, setEffective] = useState<SshEffective | null>(null);
  const [effectiveBusy, setEffectiveBusy] = useState(false);
  const [effectiveQuery, setEffectiveQuery] = useState("");
  const [note, setNote] = useState(host.note ?? "");
  const [noteBusy, setNoteBusy] = useState(false);
  const [secretKind, setSecretKind] = useState<SshSecretKind>("password");
  const [secretValue, setSecretValue] = useState("");
  const [secretBusy, setSecretBusy] = useState("");
  const [secretFormOpen, setSecretFormOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [copied, setCopied] = useState(false);

  const alias = host.alias;

  useEffect(() => {
    setTestResult(null);
    setHostKey(null);
    setScanned(null);
    setEffective(null);
    setEffectiveQuery("");
    setSecretValue("");
    setSecretFormOpen(false);
    setNote(host.note ?? "");
  }, [alias, host.note]);

  const loadHostKey = useCallback(async () => {
    try {
      const info = await api.sshHostKey(alias);
      setHostKey(info);
    } catch {
      setHostKey(null);
    }
  }, [alias]);

  useEffect(() => {
    void loadHostKey();
  }, [loadHostKey]);

  const runTest = async () => {
    setTesting(true);
    try {
      const result = await api.sshTest(alias);
      setTestResult(result);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Uji koneksi gagal.");
    } finally {
      setTesting(false);
    }
  };

  const scan = async () => {
    setHostKeyBusy("scan");
    try {
      const result = await api.sshScanHostKey(alias);
      setScanned(result.fingerprints ?? []);
      toast.info(`Menemukan ${(result.fingerprints ?? []).length} kunci host.`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Pemindaian gagal.");
    } finally {
      setHostKeyBusy("");
    }
  };

  const trust = async () => {
    setHostKeyBusy("trust");
    try {
      const result = await api.sshTrustHostKey(alias);
      toast.success(`${result.trusted} kunci host dipercaya dan ditulis ke known_hosts.`);
      setScanned(null);
      await loadHostKey();
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal mempercayai kunci host.");
    } finally {
      setHostKeyBusy("");
    }
  };

  const forget = async () => {
    setHostKeyBusy("forget");
    try {
      const result = await api.sshForgetHostKey(alias);
      toast.success(result.removed ? "Kunci host dihapus dari known_hosts." : "Tidak ada entri untuk dihapus.");
      await loadHostKey();
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menghapus kunci host.");
    } finally {
      setHostKeyBusy("");
    }
  };

  const loadEffective = async () => {
    setEffectiveBusy(true);
    try {
      setEffective(await api.sshEffective(alias));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal membaca konfigurasi efektif.");
    } finally {
      setEffectiveBusy(false);
    }
  };

  const saveNote = async () => {
    setNoteBusy(true);
    try {
      await api.sshSetNote(alias, note);
      toast.success("Catatan disimpan.");
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menyimpan catatan.");
    } finally {
      setNoteBusy(false);
    }
  };

  const saveSecret = async () => {
    if (!secretValue) {
      toast.error("Nilai kredensial tidak boleh kosong.");
      return;
    }
    setSecretBusy("save");
    try {
      await api.sshSetSecret(alias, secretKind, secretValue);
      setSecretValue("");
      setSecretFormOpen(false);
      toast.success(
        secretKind === "password" ? "Password tersimpan." : "Passphrase kunci tersimpan.",
      );
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menyimpan kredensial.");
    } finally {
      setSecretBusy("");
    }
  };

  const clearSecret = async (kind: SshSecretKind) => {
    setSecretBusy(kind);
    try {
      await api.sshClearSecret(alias, kind);
      toast.success("Kredensial dihapus.");
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menghapus kredensial.");
    } finally {
      setSecretBusy("");
    }
  };

  const copyCommand = async () => {
    try {
      await navigator.clipboard.writeText(`ssh ${alias}`);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      toast.info(`ssh ${alias}`);
    }
  };

  const toggleFavorite = async () => {
    try {
      await api.sshSetFavorite(alias, !host.favorite);
      onToggleFavorite(host);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal mengubah favorit.");
    }
  };

  const filteredEffective = useMemo(() => {
    const options = effective?.options ?? [];
    const q = effectiveQuery.trim().toLowerCase();
    if (!q) return options;
    return options.filter(
      (opt) => opt.key.toLowerCase().includes(q) || opt.value.toLowerCase().includes(q),
    );
  }, [effective, effectiveQuery]);

  const identityFile = host.identityFile ?? [];
  const options = host.options ?? [];
  const knownKeyCount = hostKey?.fingerprints?.length ?? 0;

  return (
    <div className="ssh-detail">
      <header className="ssh-detail__head">
        <div className="ssh-detail__identity">
          <h2>
            {alias}
            <button
              type="button"
              className={`ssh-star-btn ${host.favorite ? "is-on" : ""}`}
              aria-label={host.favorite ? "Hapus dari favorit" : "Tandai favorit"}
              aria-pressed={host.favorite}
              onClick={toggleFavorite}
            >
              <IconStar width={16} height={16} fill={host.favorite ? "currentColor" : "none"} />
            </button>
          </h2>
          <button type="button" className="ssh-detail__cmd" onClick={copyCommand} title="Salin perintah">
            <code>ssh {alias}</code>
            {copied ? <IconCheck width={14} height={14} /> : <IconCopy width={14} height={14} />}
          </button>
        </div>
        <div className="ssh-detail__actions">
          <Button variant="primary" size="sm" icon={<IconPlay width={15} height={15} />} onClick={() => onOpenTerminal(alias)}>
            Buka terminal
          </Button>
          <Button variant="secondary" size="sm" icon={<IconArrowUpRight width={15} height={15} />} loading={testing} onClick={runTest}>
            Uji koneksi
          </Button>
          <Button variant="secondary" size="sm" icon={<IconEdit width={15} height={15} />} onClick={() => onEdit(host)}>
            Ubah
          </Button>
          <Button variant="danger" size="sm" icon={<IconTrash width={15} height={15} />} onClick={() => setConfirmDelete(true)} disabled={!host.writable}>
            Hapus
          </Button>
        </div>
      </header>

      {!host.writable && (
        <div className="ssh-banner ssh-banner--info">
          <IconAlert width={16} height={16} />
          <div>
            <strong>Host hanya baca.</strong> Blok ini berada di <code>{host.file}</code> yang
            disertakan lewat <code>Include</code>. Ubah berkas tersebut secara manual; dashboard
            tidak pernah menulis ke file include.
          </div>
        </div>
      )}

      {host.sharedBlock && (
        <div className="ssh-banner ssh-banner--info">
          <IconAlert width={16} height={16} />
          <div>
            Satu blok <code>Host</code> ini mendefinisikan beberapa alias:{" "}
            <code>{(host.patterns ?? []).join(" ")}</code>. Perubahan pada alias ini ikut
            memengaruhi alias lain di blok yang sama.
          </div>
        </div>
      )}

      {host.secretBroken && (
        <div className="ssh-banner ssh-banner--warning">
          <IconLock width={16} height={16} />
          <div>
            <strong>Kredensial tidak dapat dibuka.</strong> <code>DASH_SESSION_SECRET</code> tampaknya
            berubah. Simpan ulang password/passphrase, atau jalankan{" "}
            <code>dashboard reset-ssh-key</code> bila memang ingin menghapus semuanya.
          </div>
        </div>
      )}

      {testResult && (
        <div className={`ssh-test ssh-test--${TEST_TONE[testResult.status] ?? "neutral"}`}>
          <Badge tone={TEST_TONE[testResult.status] ?? "neutral"}>{testResult.status}</Badge>
          <span>{testResult.message}</span>
          <span className="muted">{testResult.durationMs} ms</span>
        </div>
      )}

      <div className="ssh-detail__grid">
        <section className="ssh-panel">
          <h3 className="ssh-panel__title">Ringkasan</h3>
          <dl className="ssh-defs">
            <div>
              <dt>HostName</dt>
              <dd>
                <code>{host.hostName || host.alias}</code>
              </dd>
            </div>
            <div>
              <dt>User</dt>
              <dd>{host.user ? <code>{host.user}</code> : <span className="muted">default</span>}</dd>
            </div>
            <div>
              <dt>Port</dt>
              <dd>{host.port ? <code>{host.port}</code> : <span className="muted">22</span>}</dd>
            </div>
            {host.proxyJump && (
              <div>
                <dt>ProxyJump</dt>
                <dd>
                  <code>{host.proxyJump}</code>
                </dd>
              </div>
            )}
            <div>
              <dt>IdentityFile</dt>
              <dd>
                {identityFile.length === 0 ? (
                  <span className="muted">default / agen kunci</span>
                ) : (
                  identityFile.map((file) => <code key={file}>{file}</code>)
                )}
              </dd>
            </div>
            {host.comment && (
              <div>
                <dt>Deskripsi</dt>
                <dd>{host.comment}</dd>
              </div>
            )}
            <div>
              <dt>Berkas</dt>
              <dd>
                <code>{host.file}</code>
                <span className="muted"> · baris {host.startLine}–{host.endLine}</span>
              </dd>
            </div>
            <div>
              <dt>Pemakaian</dt>
              <dd>
                {host.connectCount > 0
                  ? `${host.connectCount}× · terakhir ${formatRelative(host.lastConnectedAt ?? "")}`
                  : "belum pernah dibuka"}
              </dd>
            </div>
          </dl>
        </section>

        <section className="ssh-panel">
          <h3 className="ssh-panel__title">
            <IconFingerprint width={15} height={15} /> Kunci host
          </h3>
          <div className="ssh-panel__row">
            <Badge tone={hostKey?.known ? "success" : "warning"}>
              {hostKey?.known ? "dipercaya" : "belum dipercaya"}
            </Badge>
            {hostKey?.hostSpec && <code className="muted">{hostKey.hostSpec}</code>}
          </div>

          {hostKey?.known && knownKeyCount > 0 && (
            <ul className="ssh-fp-list">
              {(hostKey.fingerprints ?? []).map((fp) => (
                <li key={fp.fingerprint}>
                  <code className="ssh-fp-list__type">{fp.type}</code>
                  <code className="ssh-fp-list__fp">{fp.fingerprint}</code>
                </li>
              ))}
            </ul>
          )}

          {scanned && (
            <div className="ssh-fp-scanned">
              <div className="ssh-panel__row">
                <strong>Kunci dari host saat ini</strong>
                <span className="muted">bandingkan sebelum mempercayai</span>
              </div>
              {scanned.length === 0 ? (
                <p className="muted">Host tidak memberikan kunci.</p>
              ) : (
                <ul className="ssh-fp-list">
                  {scanned.map((fp) => (
                    <li key={fp.fingerprint}>
                      <code className="ssh-fp-list__type">{fp.type}</code>
                      <code className="ssh-fp-list__fp">{fp.fingerprint}</code>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}

          <div className="ssh-panel__actions">
            <Button variant="secondary" size="sm" icon={<IconSearch width={14} height={14} />} loading={hostKeyBusy === "scan"} onClick={scan}>
              Pindai kunci
            </Button>
            <Button variant="secondary" size="sm" icon={<IconCheck width={14} height={14} />} loading={hostKeyBusy === "trust"} onClick={trust}>
              Percayai &amp; tulis
            </Button>
            {hostKey?.known && (
              <Button variant="ghost" size="sm" icon={<IconTrash width={14} height={14} />} loading={hostKeyBusy === "forget"} onClick={forget}>
                Lupakan
              </Button>
            )}
          </div>
          <p className="ssh-panel__note">
            Pada koneksi pertama, sesi baru akan meminta konfirmasi kunci host seperti{" "}
            <code>ssh</code> biasa. Mempercayai di sini berarti melewati langkah tersebut.
          </p>
        </section>

        <section className="ssh-panel">
          <h3 className="ssh-panel__title">
            <IconLock width={15} height={15} /> Kredensial tersimpan
          </h3>
          <p className="ssh-panel__note">
            Dipakai otomatis saat membuka sesi. Nilai tidak pernah ditampilkan kembali dan tidak
            tersimpan sebagai teks biasa.
          </p>

          <ul className="ssh-secret-list">
            <li>
              <div>
                <strong>Password SSH</strong>
                <span className="muted">
                  {host.hasPassword ? "tersimpan" : "belum ada"}
                  {host.hasPassword && host.secretBroken ? " (rusak)" : ""}
                </span>
              </div>
              <div className="ssh-secret-list__actions">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSecretKind("password");
                    setSecretFormOpen(true);
                  }}
                >
                  {host.hasPassword ? "Ganti" : "Simpan"}
                </Button>
                {host.hasPassword && (
                  <Button variant="ghost" size="sm" loading={secretBusy === "password"} onClick={() => clearSecret("password")}>
                    Hapus
                  </Button>
                )}
              </div>
            </li>
            <li>
              <div>
                <strong>Passphrase kunci</strong>
                <span className="muted">
                  {host.hasPassphrase ? "tersimpan" : "belum ada"}
                  {host.hasPassphrase && host.secretBroken ? " (rusak)" : ""}
                </span>
              </div>
              <div className="ssh-secret-list__actions">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSecretKind("passphrase");
                    setSecretFormOpen(true);
                  }}
                >
                  {host.hasPassphrase ? "Ganti" : "Simpan"}
                </Button>
                {host.hasPassphrase && (
                  <Button variant="ghost" size="sm" loading={secretBusy === "passphrase"} onClick={() => clearSecret("passphrase")}>
                    Hapus
                  </Button>
                )}
              </div>
            </li>
          </ul>

          {secretFormOpen && (
            <div className="ssh-secret-form">
              <label className="field">
                <span>{secretKind === "password" ? "Password" : "Passphrase kunci"}</span>
                <input
                  type="password"
                  value={secretValue}
                  onChange={(e) => setSecretValue(e.target.value)}
                  autoComplete="new-password"
                  autoFocus
                />
              </label>
              <div className="ssh-panel__actions">
                <Button variant="primary" size="sm" loading={secretBusy === "save"} onClick={saveSecret} disabled={!secretValue}>
                  Simpan
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSecretFormOpen(false);
                    setSecretValue("");
                  }}
                >
                  Batal
                </Button>
              </div>
            </div>
          )}

          {keys.length > 0 && (
            <p className="ssh-panel__note">
              Kunci di <code>~/.ssh</code>: {keys.map((key) => key.name).join(", ")}
            </p>
          )}
        </section>

        <section className="ssh-panel">
          <h3 className="ssh-panel__title">Catatan</h3>
          <textarea
            className="ssh-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
            placeholder="Catatan pribadi: fungsi server, pemilik, jadwal maintenance..."
          />
          <div className="ssh-panel__actions">
            <Button variant="secondary" size="sm" loading={noteBusy} onClick={saveNote} disabled={note === (host.note ?? "")}>
              Simpan catatan
            </Button>
          </div>
          <p className="ssh-panel__note">
            Catatan hanya tersimpan di dashboard, tidak ditulis ke <code>~/.ssh/config</code>.
          </p>
        </section>

        <section className="ssh-panel ssh-panel--wide">
          <h3 className="ssh-panel__title">Konfigurasi efektif</h3>
          <p className="ssh-panel__note">
            Hasil <code>ssh -G {alias}</code>: nilai akhir setelah semua Include, wildcard, dan
            nilai bawaan ssh. Inilah yang benar-benar dipakai saat menyambung.
          </p>
          <div className="ssh-panel__actions">
            <Button variant="secondary" size="sm" icon={<IconRefresh width={14} height={14} />} loading={effectiveBusy} onClick={loadEffective}>
              {effective ? "Muat ulang" : "Tampilkan"}
            </Button>
          </div>
          {effective && (
            <>
              <label className="field ssh-effective__search">
                <span className="sr-only">Cari opsi</span>
                <input
                  value={effectiveQuery}
                  onChange={(e) => setEffectiveQuery(e.target.value)}
                  placeholder="Cari opsi atau nilai..."
                  spellCheck={false}
                />
              </label>
              <div className="ssh-effective">
                {filteredEffective.map((opt) => (
                  <div className="ssh-effective__row" key={`${opt.key}-${opt.value}`}>
                    <code className="ssh-effective__key">{opt.key}</code>
                    <code className="ssh-effective__value">{opt.value || '""'}</code>
                  </div>
                ))}
              </div>
              <p className="muted">{filteredEffective.length} opsi</p>
            </>
          )}
        </section>

        <section className="ssh-panel ssh-panel--wide">
          <h3 className="ssh-panel__title">Opsi lanjutan</h3>
          {options.length === 0 ? (
            <p className="muted">Tidak ada direktif tambahan pada blok ini.</p>
          ) : (
            <div className="ssh-effective">
              {options.map((opt) => (
                <div className="ssh-effective__row" key={`${opt.key}-${opt.value}`}>
                  <code className="ssh-effective__key">{opt.key}</code>
                  <code className="ssh-effective__value">{opt.value || '""'}</code>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>

      <footer className="ssh-detail__foot">
        <span className="muted">
          {host.lastConnectedAt
            ? `Terakhir dibuka ${formatRelative(host.lastConnectedAt)}`
            : "Belum pernah dibuka dari dashboard"}
        </span>
        <span className="muted mono">{hostEndpoint(host)}</span>
      </footer>

      <ConfirmDialog
        open={confirmDelete}
        title={`Hapus host ${alias}?`}
        message={
          <div>
            <p>
              Blok <code>Host {alias}</code> akan dihapus dari <code>~/.ssh/config</code>, bersama
              kredensial dan catatan yang tersimpan untuk host ini.
            </p>
            <p className="muted">
              Cadangan file dibuat otomatis sebagai <code>~/.ssh/config.bak.&lt;waktu&gt;</code>.
            </p>
          </div>
        }
        confirmLabel="Hapus host"
        destructive
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => {
          setConfirmDelete(false);
          onDelete(host);
        }}
      />
    </div>
  );
}
