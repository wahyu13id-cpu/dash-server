import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../lib/api";
import type { OpsJob, PackageStatus } from "../lib/types";
import { useOps } from "../context/OpsContext";
import { useToast } from "../context/ToastContext";
import { Button } from "../components/Button";
import { Badge, Card } from "../components/Card";
import { ConfirmDialog } from "../components/Modal";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { IconAlert, IconBox, IconRefresh } from "../components/Icons";
import { formatDateTime, formatRelative } from "../lib/format";

type PendingUpgrade = "upgrade" | "dist" | null;

export function PackagesPage() {
  const toast = useToast();
  const { availability, refresh: refreshAvailability } = useOps();

  const [status, setStatus] = useState<PackageStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [job, setJob] = useState<OpsJob | null>(null);
  const [pending, setPending] = useState<PendingUpgrade>(null);
  const pollRef = useRef<number | null>(null);

  const load = useCallback(async (force = false, background = false) => {
    if (!background) setLoading(true);
    try {
      const value = await api.opsPackages(force);
      setStatus(value);
      setError("");
    } catch (err) {
      if (!background) setError(err instanceof Error ? err.message : "Gagal memuat status paket.");
    } finally {
      if (!background) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Stop polling when the page goes away.
  useEffect(() => {
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
    };
  }, []);

  const watchJob = useCallback(
    (started: OpsJob) => {
      setJob(started);
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
      const tick = async () => {
        try {
          const current = await api.opsJob(started.id);
          setJob(current);
          if (!current.running) {
            if (pollRef.current !== null) window.clearInterval(pollRef.current);
            pollRef.current = null;
            if (current.exitCode === 0) {
              toast.success(`${current.command}: selesai.`);
            } else {
              toast.error(`${current.command}: berhenti dengan kode ${current.exitCode}.`);
            }
            // The upgrade invalidates the cached picture on the server.
            await load(true, true);
            refreshAvailability();
          }
        } catch {
          if (pollRef.current !== null) window.clearInterval(pollRef.current);
          pollRef.current = null;
        }
      };
      void tick();
      pollRef.current = window.setInterval(() => void tick(), 1500);
    },
    [load, refreshAvailability, toast],
  );

  const refreshIndex = useCallback(async () => {
    try {
      watchJob(await api.opsRefreshPackages());
      toast.success("Memperbarui daftar paket di latar belakang.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal memperbarui daftar paket.");
    }
  }, [toast, watchJob]);

  const startUpgrade = useCallback(
    async (dist: boolean) => {
      try {
        watchJob(await api.opsUpgradePackages(dist));
        toast.success("Peningkatan paket dimulai di latar belakang.");
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Gagal memulai peningkatan paket.");
      }
    },
    [toast, watchJob],
  );

  if (availability && !availability.packageManager) {
    return (
      <div className="page">
        <Card title="Pembaruan paket">
          <EmptyState
            icon={<IconBox width={22} height={22} />}
            title="Manajer paket tidak dikenali"
            description="Halaman ini mendukung apt (Debian/Ubuntu) dan dnf/yum (RHEL/Fedora)."
          />
        </Card>
      </div>
    );
  }

  if (loading && !status) {
    return (
      <div className="page">
        <Spinner label="Memeriksa pembaruan paket..." />
      </div>
    );
  }

  if (error && !status) {
    return (
      <div className="page">
        <Card title="Pembaruan paket">
          <ErrorState message={error} onRetry={() => void load()} />
        </Card>
      </div>
    );
  }

  const upgradable = status?.upgradable ?? [];
  const jobRunning = job?.running ?? false;

  return (
    <div className="page">
      <div className="page__intro">
        <div>
          <h2>Pembaruan paket</h2>
          <p>
            Manajer paket <strong>{status?.manager || "-"}</strong> · diperiksa{" "}
            {status ? formatRelative(status.checkedAt) : "-"}
            {status?.stale ? " (data lama, pemeriksaan ulang gagal)" : ""}
          </p>
        </div>
        <div className="page__intro-actions">
          <Button
            variant="ghost"
            onClick={() => void refreshIndex()}
            disabled={jobRunning}
            icon={<IconRefresh width={16} height={16} />}
          >
            Perbarui daftar paket
          </Button>
          <Button variant="ghost" onClick={() => void load(true)} disabled={jobRunning}>
            Periksa ulang
          </Button>
        </div>
      </div>

      <div className="ops-summary">
        <div className="ops-stat">
          <span className="ops-stat__label">Pembaruan tersedia</span>
          <span className="ops-stat__value">{status?.count ?? 0}</span>
        </div>
        <div className="ops-stat">
          <span className="ops-stat__label">Pembaruan keamanan</span>
          <span className={`ops-stat__value ${status?.securityCount ? "is-warn" : ""}`}>
            {status?.securityCount ?? 0}
          </span>
        </div>
        <div className="ops-stat">
          <span className="ops-stat__label">Paket akan dihapus</span>
          <span className={`ops-stat__value ${status?.removals ? "is-warn" : ""}`}>
            {status?.removals ?? 0}
          </span>
        </div>
        <div className="ops-stat">
          <span className="ops-stat__label">Reboot diperlukan</span>
          <span className={`ops-stat__value ${status?.rebootRequired ? "is-warn" : ""}`}>
            {status?.rebootRequired ? "ya" : "tidak"}
          </span>
        </div>
      </div>

      {status?.rebootRequired && (
        <div className="ops-notice ops-notice--warn">
          <IconAlert width={18} height={18} />
          <div>
            Sistem menandai perlu reboot setelah pembaruan
            {status.rebootPackages && status.rebootPackages.length > 0
              ? ` (${status.rebootPackages.slice(0, 6).join(", ")}${status.rebootPackages.length > 6 ? "…" : ""})`
              : ""}
            . Jadwalkan reboot saat tidak ada sesi aktif.
          </div>
        </div>
      )}

      {status?.error && (
        <div className="ops-notice ops-notice--warn">
          <IconAlert width={18} height={18} />
          <div>Pemeriksaan terakhir gagal: {status.error}</div>
        </div>
      )}

      <Card
        title="Paket yang bisa diperbarui"
        subtitle={
          upgradable.length === 0
            ? "Tidak ada pembaruan tertunda."
            : `${upgradable.length} paket siap diperbarui.`
        }
        actions={
          <>
            <Button
              variant="primary"
              disabled={jobRunning || upgradable.length === 0}
              onClick={() => setPending("upgrade")}
            >
              Tingkatkan semua
            </Button>
            <Button
              variant="ghost"
              disabled={jobRunning}
              onClick={() => setPending("dist")}
              title="dist-upgrade juga boleh memasang atau menghapus paket"
            >
              dist-upgrade
            </Button>
          </>
        }
        padded={upgradable.length === 0}
      >
        {upgradable.length === 0 ? (
          <EmptyState
            icon={<IconBox width={22} height={22} />}
            title="Sistem sudah mutakhir"
            description="Semua paket terpasang pada versi terbaru yang tersedia."
          />
        ) : (
          <div className="ops-table ops-table--scroll">
            <div className="ops-head" style={{ gridTemplateColumns: "minmax(0, 1.4fr) 150px 150px minmax(0, 1fr)" }}>
              <span>Paket</span>
              <span>Terpasang</span>
              <span>Kandidat</span>
              <span>Sumber</span>
            </div>
            {upgradable.map((pkg) => (
              <div
                className="ops-row"
                key={pkg.name}
                style={{ gridTemplateColumns: "minmax(0, 1.4fr) 150px 150px minmax(0, 1fr)" }}
              >
                <span className="ops-row__title">{pkg.name}</span>
                <span className="ops-row__mono">{pkg.current || "—"}</span>
                <span className="ops-row__mono">{pkg.candidate || "—"}</span>
                <span className="ops-row__badges">
                  {pkg.security && <Badge tone="danger">keamanan</Badge>}
                  <span className="ops-row__muted">{pkg.origin || "-"}</span>
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>

      {job && (
        <Card
          title={`Keluaran: ${job.command}`}
          subtitle={`${job.running ? "berjalan" : `selesai (kode ${job.exitCode})`} · dimulai ${formatDateTime(job.startedAt)}`}
          actions={
            <Button variant="ghost" size="sm" onClick={() => setJob(null)} disabled={job.running}>
              Tutup
            </Button>
          }
        >
          {job.running && <Spinner label="Perintah sedang berjalan..." />}
          <pre className="job-output">
            {job.output && job.output.length > 0
              ? job.output.join("\n")
              : job.running
                ? "(menunggu keluaran...)"
                : "(tidak ada keluaran)"}
          </pre>
          {job.truncated && <p className="muted">Keluaran dipotong pada 4000 baris terakhir.</p>}
          {job.error && !job.running && <p className="form-error">{job.error}</p>}
        </Card>
      )}

      <ConfirmDialog
        open={pending !== null}
        title={pending === "dist" ? "Jalankan dist-upgrade" : "Tingkatkan semua paket"}
        message={
          pending === "dist"
            ? `Jalankan dist-upgrade? Tindakan ini dapat memasang paket baru dan menghapus paket yang tidak lagi diperlukan${status?.removals ? ` (simulasi sekarang: ${status.removals} paket dihapus)` : ""}.`
            : `Pasang ${upgradable.length} pembaruan sekarang? Proses berjalan di latar belakang dan keluarannya tampil di halaman ini.`
        }
        confirmLabel={pending === "dist" ? "Jalankan dist-upgrade" : "Tingkatkan"}
        destructive={pending === "dist"}
        busy={jobRunning}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          const mode = pending;
          setPending(null);
          if (mode) await startUpgrade(mode === "dist");
        }}
      />
    </div>
  );
}
