import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { ProcessInfo, ProcessList, ProcessSort } from "../lib/types";
import { useOps } from "../context/OpsContext";
import { useToast } from "../context/ToastContext";
import { Button, IconButton } from "../components/Button";
import { Badge, Card } from "../components/Card";
import { ConfirmDialog } from "../components/Modal";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { IconActivity, IconRefresh, IconSearch, IconStop } from "../components/Icons";
import { formatBytes, formatDuration, formatRelative } from "../lib/format";
import { processStateLabel } from "../lib/ops";

const SORTS: { key: ProcessSort; label: string }[] = [
  { key: "cpu", label: "CPU" },
  { key: "mem", label: "Memori" },
  { key: "time", label: "Waktu jalan" },
  { key: "name", label: "Nama" },
  { key: "pid", label: "PID" },
];

const COLS = "78px 120px minmax(0, 2.6fr) 74px 66px 84px 74px 96px 168px";

export function ProcessesPage() {
  const toast = useToast();
  const { availability } = useOps();

  const [data, setData] = useState<ProcessList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<ProcessSort>("cpu");
  const [auto, setAuto] = useState(true);
  const [busyPid, setBusyPid] = useState(0);
  const [confirmKill, setConfirmKill] = useState<{ pid: number; name: string } | null>(null);

  const load = useCallback(
    async (background = false) => {
      if (!background) setLoading(true);
      try {
        const list = await api.opsProcesses({ sort });
        setData(list);
        setError("");
      } catch (err) {
        if (!background) setError(err instanceof Error ? err.message : "Gagal memuat daftar proses.");
      } finally {
        if (!background) setLoading(false);
      }
    },
    [sort],
  );

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!auto) return;
    const timer = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(timer);
  }, [auto, load]);

  const processes = useMemo(() => {
    const all = data?.processes ?? [];
    const needle = query.trim().toLowerCase();
    if (!needle) return all;
    return all.filter((p) =>
      `${p.pid} ${p.name} ${p.command} ${p.user}`.toLowerCase().includes(needle),
    );
  }, [data, query]);

  const nice = useCallback(
    async (proc: ProcessInfo, value: number) => {
      setBusyPid(proc.pid);
      try {
        await api.opsProcessNice(proc.pid, value);
        toast.success(`Prioritas ${proc.name} (PID ${proc.pid}) diubah ke ${value}.`);
        await load(true);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Gagal mengubah prioritas.");
      } finally {
        setBusyPid(0);
      }
    },
    [load, toast],
  );

  const signal = useCallback(
    async (proc: { pid: number; name: string }, sig: string) => {
      setBusyPid(proc.pid);
      try {
        await api.opsProcessSignal(proc.pid, sig);
        toast.success(`Sinyal ${sig} dikirim ke ${proc.name} (PID ${proc.pid}).`);
        await load(true);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Gagal mengirim sinyal.");
      } finally {
        setBusyPid(0);
      }
    },
    [load, toast],
  );

  if (availability && !availability.processes) {
    return (
      <div className="page">
        <Card title="Proses">
          <EmptyState
            icon={<IconActivity width={22} height={22} />}
            title="procfs tidak tersedia di host ini"
            description="Daftar proses dibaca dari /proc, yang hanya ada di Linux."
          />
        </Card>
      </div>
    );
  }

  if (loading && !data) {
    return (
      <div className="page">
        <Spinner label="Memuat daftar proses..." />
      </div>
    );
  }

  if (error && !data) {
    return (
      <div className="page">
        <Card title="Proses">
          <ErrorState message={error} onRetry={() => void load()} />
        </Card>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page__intro">
        <div>
          <h2>Proses</h2>
          <p>
            {data?.total ?? 0} proses berjalan di {data?.cpuCores ?? 0} inti CPU · memori total{" "}
            {formatBytes(data?.memTotalBytes ?? 0)}.
          </p>
        </div>
        <div className="page__intro-actions">
          <Button variant="ghost" onClick={() => void load()} icon={<IconRefresh width={16} height={16} />}>
            Muat ulang
          </Button>
        </div>
      </div>

      <Card
        padded={false}
        actions={
          <label className="ops-toggle">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
            <span>Otomatis 3 detik</span>
          </label>
        }
        title="Daftar proses"
        subtitle={
          query
            ? `${processes.length} dari ${data?.total ?? 0} proses cocok`
            : "Diurutkan di server; sinyal dan prioritas dicatat di log audit."
        }
      >
        <div className="ops-toolbar ops-toolbar--padded">
          <span className="ops-search">
            <IconSearch width={15} height={15} />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Cari PID, nama, perintah, atau pengguna..."
              aria-label="Cari proses"
              spellCheck={false}
            />
          </span>
          <div className="ops-filters" role="group" aria-label="Urutkan">
            <span className="ops-filters__label">Urut:</span>
            {SORTS.map((s) => (
              <button
                key={s.key}
                type="button"
                className={`ops-chip ${sort === s.key ? "ops-chip--active" : ""}`}
                onClick={() => setSort(s.key)}
              >
                {s.label}
              </button>
            ))}
          </div>
        </div>

        {processes.length === 0 ? (
          <div className="ops-pad">
            <EmptyState
              icon={<IconActivity width={22} height={22} />}
              title="Tidak ada proses yang cocok"
              description="Ubah kata kunci pencarian."
            />
          </div>
        ) : (
          <div className="ops-table ops-table--scroll">
            <div className="ops-head" style={{ gridTemplateColumns: COLS }}>
              <span className="is-num">PID</span>
              <span>Pengguna</span>
              <span>Perintah</span>
              <span className="is-num">CPU</span>
              <span className="is-num">Mem</span>
              <span className="is-num">RSS</span>
              <span>Status</span>
              <span>Waktu</span>
              <span className="is-num">Aksi</span>
            </div>
            {processes.map((proc) => (
              <div className="ops-row" key={proc.pid} style={{ gridTemplateColumns: COLS }}>
                <span className="ops-row__mono is-num">{proc.pid}</span>
                <span className="ops-row__user" title={`UID ${proc.uid}`}>
                  {proc.user}
                </span>
                <span className="ops-row__primary ops-row__primary--static" title={proc.command}>
                  <span className="ops-row__title">{proc.name}</span>
                  <span className="ops-row__sub">{proc.command}</span>
                </span>
                <span className={`ops-row__num ${proc.cpuPercent >= 50 ? "is-hot" : ""}`}>
                  {proc.cpuPercent.toFixed(1)}%
                </span>
                <span className="ops-row__num">{proc.memPercent.toFixed(1)}%</span>
                <span className="ops-row__num ops-row__num--muted">{formatBytes(proc.rssBytes)}</span>
                <span>
                  <Badge tone={proc.state === "Z" ? "danger" : proc.state === "R" ? "success" : "neutral"}>
                    {processStateLabel(proc.state)}
                  </Badge>
                </span>
                <span className="ops-row__muted" title={formatRelative(proc.startedAt)}>
                  {formatDuration(proc.elapsedSec)}
                </span>
                <span className="ops-row__actions">
                  <select
                    className="ops-select"
                    value=""
                    disabled={busyPid === proc.pid || proc.pid <= 1}
                    onChange={(e) => {
                      const value = Number(e.target.value);
                      e.currentTarget.value = "";
                      if (!Number.isNaN(value)) void nice(proc, value);
                    }}
                    aria-label={`Ubah prioritas PID ${proc.pid}`}
                    title={`Prioritas sekarang: ${proc.nice}`}
                  >
                    <option value="">nice {proc.nice}</option>
                    <option value="-5">Tinggikan (−5)</option>
                    <option value="0">Normal (0)</option>
                    <option value="10">Turunkan (10)</option>
                    <option value="19">Terendah (19)</option>
                  </select>
                  <IconButton
                    label="Kirim sinyal TERM (hentikan dengan sopan)"
                    disabled={busyPid === proc.pid || proc.pid <= 1}
                    onClick={() => setConfirmKill({ pid: proc.pid, name: proc.name })}
                  >
                    <IconStop width={15} height={15} />
                  </IconButton>
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>

      <ConfirmDialog
        open={confirmKill !== null}
        title="Hentikan proses"
        message={
          confirmKill
            ? `Kirim SIGTERM ke ${confirmKill.name} (PID ${confirmKill.pid})? Proses akan diberi kesempatan menutup diri dengan rapi.`
            : ""
        }
        confirmLabel="Kirim SIGTERM"
        destructive
        busy={busyPid !== 0}
        onCancel={() => setConfirmKill(null)}
        onConfirm={async () => {
          if (!confirmKill) return;
          const target = confirmKill;
          setConfirmKill(null);
          await signal(target, "TERM");
        }}
      />
    </div>
  );
}
