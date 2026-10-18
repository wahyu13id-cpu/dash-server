import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import type { AuditEntry, Stats, SystemSnapshot } from "../lib/types";
import { formatBytes, formatDateTime, formatDuration, formatRelative } from "../lib/format";
import { useAuth } from "../context/AuthContext";
import { Badge, Card } from "../components/Card";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import {
  CoreStrip,
  DiskList,
  MetricCard,
  NetworkList,
  ProcessTable,
  Sparkline,
  formatRate,
} from "../components/Monitoring";
import {
  IconActivity,
  IconCpu,
  IconDisk,
  IconMemory,
  IconNetwork,
  IconRefresh,
  IconServer,
} from "../components/Icons";

const REFRESH_MS = 3000;

export function DashboardPage() {
  const { user } = useAuth();
  const [stats, setStats] = useState<Stats | null>(null);
  const [system, setSystem] = useState<SystemSnapshot | null>(null);
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [live, setLive] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  const loadSystem = useCallback(async () => {
    try {
      const res = await api.system();
      if (res.ready && res.snapshot) {
        setSystem(res.snapshot);
        setUpdatedAt(new Date());
        setLive(true);
      }
    } catch {
      setLive(false);
    }
  }, []);

  const load = useCallback(async () => {
    setError("");
    try {
      const [s, activity, sys] = await Promise.all([api.stats(), api.audit(), api.system()]);
      setStats(s);
      setEntries(activity.entries);
      if (sys.ready && sys.snapshot) {
        setSystem(sys.snapshot);
        setUpdatedAt(new Date());
        setLive(true);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal memuat data dashboard.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll the lightweight metrics endpoint while the tab is visible.
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void loadSystem();
      }
    }, REFRESH_MS);
    return () => window.clearInterval(id);
  }, [loadSystem]);

  if (loading && !stats && !system) {
    return <Spinner label="Memuat dashboard..." />;
  }
  if (error && !stats && !system) {
    return <ErrorState message={error} onRetry={load} />;
  }

  const cpu = system?.cpu;
  const mem = system?.memory;
  const disks = system?.disks;
  const net = system?.network;
  const procs = system?.processes;

  return (
    <div className="page">
      <div className="page__intro">
        <div>
          <h2>Selamat datang{user?.displayName ? `, ${user.displayName}` : ""}</h2>
          <p>Pemantauan server langsung: CPU, memori, disk, jaringan, dan proses teratas.</p>
        </div>
        <div className="page__intro-actions">
          <span className={`live-badge ${live ? "is-live" : "is-stale"}`} title={updatedAt ? `Diperbarui ${formatDateTime(updatedAt.toISOString())}` : ""}>
            <span className="live-badge__dot" aria-hidden="true" />
            {live ? "Live" : "Terputus"}
            {updatedAt && <span className="live-badge__time">· {updatedAt.toLocaleTimeString()}</span>}
          </span>
          <button className="btn btn--secondary btn--sm" onClick={load} type="button">
            <IconRefresh width={16} height={16} />
            <span>Muat ulang</span>
          </button>
        </div>
      </div>

      {system ? (
        <div className="metric-grid">
          <MetricCard
            icon={<IconCpu width={18} height={18} />}
            label="CPU"
            value={cpu ? `${cpu.usagePercent.toFixed(1)}%` : "-"}
            percent={cpu?.usagePercent}
            sub={
              cpu && (
                <>
                  {cpu.cores} core · load {system.load.load1.toFixed(2)} / {system.load.load5.toFixed(2)} /{" "}
                  {system.load.load15.toFixed(2)}
                </>
              )
            }
          >
            {cpu && <CoreStrip perCore={cpu.perCore} />}
            {cpu && <Sparkline values={cpu.history} height={40} />}
          </MetricCard>

          <MetricCard
            icon={<IconMemory width={18} height={18} />}
            label="Memori"
            value={mem ? `${mem.usedPercent.toFixed(1)}%` : "-"}
            percent={mem?.usedPercent}
            sub={mem && `${formatBytes(mem.usedBytes)} / ${formatBytes(mem.totalBytes)}`}
          >
            {mem && (
              <div className="metric-card__detail">
                <span>Tersedia {formatBytes(mem.availableBytes)}</span>
                <span>
                  Cache {formatBytes(mem.cachedBytes)}
                </span>
                {mem.swapTotalBytes > 0 && (
                  <span>
                    Swap {formatBytes(mem.swapUsedBytes)} / {formatBytes(mem.swapTotalBytes)}
                  </span>
                )}
              </div>
            )}
          </MetricCard>

          <MetricCard
            icon={<IconDisk width={18} height={18} />}
            label="Disk"
            value={disks ? `${disks.usedPercent.toFixed(1)}%` : "-"}
            percent={disks?.usedPercent}
            sub={disks && `${formatBytes(disks.usedBytes)} / ${formatBytes(disks.totalBytes)}`}
          >
            {disks && (
              <div className="metric-card__detail">
                <span>{disks.filesystems.length} filesystem</span>
                <span>Bebas {formatBytes(disks.freeBytes)}</span>
              </div>
            )}
          </MetricCard>

          <MetricCard
            icon={<IconNetwork width={18} height={18} />}
            label="Jaringan"
            value={net ? `↓ ${formatRate(net.rxBytesPerSec)}` : "-"}
            sub={net && `↑ ${formatRate(net.txBytesPerSec)}`}
          >
            {net && (
              <div className="metric-card__detail">
                <span>Total turun {formatBytes(net.rxTotalBytes)}</span>
                <span>Total naik {formatBytes(net.txTotalBytes)}</span>
              </div>
            )}
          </MetricCard>
        </div>
      ) : (
        <Card title="Pemantauan server" subtitle="Mengumpulkan metrik pertama...">
          <Spinner label="Menunggu sampel metrik host..." />
        </Card>
      )}

      {procs && (
        <div className="grid grid--2">
          <Card title="Top 10 CPU" subtitle={`${procs.total} proses berjalan`}>
            <ProcessTable rows={procs.byCpu} metric="cpu" />
          </Card>
          <Card title="Top 10 Memori" subtitle="Berdasarkan RSS (memori fisik)">
            <ProcessTable rows={procs.byMemory} metric="memory" />
          </Card>
        </div>
      )}

      <div className="grid grid--2">
        <Card title="Penyimpanan" subtitle="Penggunaan per filesystem terpasang.">
          {disks ? <DiskList disks={disks.filesystems} /> : <p className="muted">Menunggu data...</p>}
        </Card>

        <Card title="Jaringan" subtitle="Lalu lintas per antarmuka (loopback dikecualikan dari total).">
          {net ? <NetworkList network={net} /> : <p className="muted">Menunggu data...</p>}
        </Card>
      </div>

      <div className="grid grid--2">
        <Card
          title="Info server"
          subtitle={system ? `${system.host.hostname} · ${system.host.arch}` : undefined}
          actions={
            <span className="metric-card__icon">
              <IconServer width={18} height={18} />
            </span>
          }
        >
          <dl className="info-list">
            {system && (
              <>
                <div>
                  <dt>Hostname</dt>
                  <dd>{system.host.hostname || "-"}</dd>
                </div>
                <div>
                  <dt>Kernel</dt>
                  <dd>{system.host.kernel || "-"}</dd>
                </div>
                <div>
                  <dt>Uptime sistem</dt>
                  <dd>{formatDuration(system.host.uptimeSec)}</dd>
                </div>
              </>
            )}
            <div>
              <dt>CPU</dt>
              <dd>{cpu ? system?.cpu.model || `${cpu.cores} core` : "-"}</dd>
            </div>
            {stats && (
              <>
                <div>
                  <dt>Uptime aplikasi</dt>
                  <dd>{formatDuration(stats.uptimeSec)}</dd>
                </div>
                <div>
                  <dt>Sesi terminal</dt>
                  <dd>
                    {stats.liveTerminals} / {stats.maxTerminals} <span className="muted">({stats.myTerminals} milik Anda)</span>
                  </dd>
                </div>
                <div>
                  <dt>Entri di root</dt>
                  <dd title={stats.workspaceRoot}>
                    {stats.rootEntries} <span className="muted">{stats.workspaceRoot}</span>
                  </dd>
                </div>
                <div>
                  <dt>Batas file</dt>
                  <dd>
                    {formatBytes(stats.maxReadBytes)} baca · {formatBytes(stats.maxWriteBytes)} tulis
                  </dd>
                </div>
                <div>
                  <dt>Waktu server</dt>
                  <dd>{formatDateTime(stats.serverTime)}</dd>
                </div>
              </>
            )}
          </dl>
        </Card>

        <Card
          title="Aktivitas terbaru"
          subtitle="Jejak audit tindakan penting."
          actions={
            <span className="metric-card__icon">
              <IconActivity width={18} height={18} />
            </span>
          }
        >
          {entries.length === 0 ? (
            <EmptyState title="Belum ada aktivitas" description="Aksi penting akan tercatat di sini." />
          ) : (
            <ul className="activity-list">
              {entries.slice(0, 8).map((entry) => (
                <li key={entry.id} className="activity-item">
                  <span className={`activity-item__dot ${entry.success ? "is-ok" : "is-fail"}`} aria-hidden="true" />
                  <div className="activity-item__body">
                    <div className="activity-item__top">
                      <code>{entry.action}</code>
                      {entry.target && (
                        <span className="activity-item__target" title={entry.target}>
                          {entry.target}
                        </span>
                      )}
                    </div>
                    <div className="activity-item__meta">
                      {entry.username && <span>{entry.username}</span>}
                      <span>{formatRelative(entry.createdAt)}</span>
                    </div>
                  </div>
                  <Badge tone={entry.success ? "success" : "danger"}>{entry.success ? "ok" : "gagal"}</Badge>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}
