import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { ServiceDetail, ServiceList, ServiceUnit } from "../lib/types";
import { useOps } from "../context/OpsContext";
import { useToast } from "../context/ToastContext";
import { Button, IconButton } from "../components/Button";
import { Badge, Card } from "../components/Card";
import { ConfirmDialog, Modal } from "../components/Modal";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { IconCog, IconPlay, IconRefresh, IconSearch, IconStop } from "../components/Icons";
import { formatBytes, formatDateTime } from "../lib/format";
import { firstLine, priorityLabel, serviceActionLabel } from "../lib/ops";

type StateFilter = "all" | "running" | "stopped" | "failed";

const STATE_FILTERS: { key: StateFilter; label: string }[] = [
  { key: "all", label: "Semua" },
  { key: "running", label: "Berjalan" },
  { key: "stopped", label: "Berhenti" },
  { key: "failed", label: "Gagal" },
];

/** Maps a unit to the badges the table shows. */
function unitState(unit: ServiceUnit): { tone: "success" | "danger" | "warning" | "neutral"; label: string } {
  if (unit.active === "failed" || unit.sub === "failed") return { tone: "danger", label: "gagal" };
  if (unit.active === "activating") return { tone: "warning", label: "menyala" };
  if (unit.active === "deactivating") return { tone: "warning", label: "mematikan" };
  if (unit.active === "active") {
    return { tone: "success", label: unit.sub === "exited" ? "selesai" : "berjalan" };
  }
  if (unit.load === "not-found" || unit.load === "masked") return { tone: "neutral", label: unit.load };
  return { tone: "neutral", label: "berhenti" };
}

function matchesFilter(unit: ServiceUnit, filter: StateFilter): boolean {
  switch (filter) {
    case "running":
      return unit.active === "active" || unit.active === "activating";
    case "stopped":
      return unit.active === "inactive" || unit.active === "deactivating";
    case "failed":
      return unit.active === "failed" || unit.sub === "failed";
    default:
      return true;
  }
}

export function ServicesPage() {
  const toast = useToast();
  const { availability } = useOps();

  const [data, setData] = useState<ServiceList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<StateFilter>("all");
  const [auto, setAuto] = useState(true);
  const [busyUnit, setBusyUnit] = useState("");
  const [detailUnit, setDetailUnit] = useState<string | null>(null);
  const [pending, setPending] = useState<{ unit: string; action: string } | null>(null);

  const load = useCallback(
    async (background = false) => {
      if (!background) setLoading(true);
      try {
        const list = await api.opsServices();
        setData(list);
        setError("");
      } catch (err) {
        if (!background) setError(err instanceof Error ? err.message : "Gagal memuat daftar layanan.");
      } finally {
        if (!background) setLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!auto) return;
    const timer = window.setInterval(() => void load(true), 5000);
    return () => window.clearInterval(timer);
  }, [auto, load]);

  const units = useMemo(() => {
    const all = data?.units ?? [];
    const needle = query.trim().toLowerCase();
    return all.filter((u) => {
      if (!matchesFilter(u, filter)) return false;
      if (!needle) return true;
      return `${u.name} ${u.description}`.toLowerCase().includes(needle);
    });
  }, [data, filter, query]);

  const runAction = useCallback(
    async (unit: string, action: string) => {
      setBusyUnit(unit);
      try {
        const res = await api.opsServiceAction(unit, action);
        toast.success(`${serviceActionLabel(action)} ${unit}: ${firstLine(res.output)}`);
        await load(true);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Aksi layanan gagal.");
      } finally {
        setBusyUnit("");
      }
    },
    [load, toast],
  );

  if (availability && !availability.systemd) {
    return (
      <div className="page">
        <Card title="Layanan sistem">
          <EmptyState
            icon={<IconCog width={22} height={22} />}
            title="systemd tidak tersedia di host ini"
            description="Manajemen layanan memerlukan systemd sebagai init system."
          />
        </Card>
      </div>
    );
  }

  if (loading && !data) {
    return (
      <div className="page">
        <Spinner label="Memuat daftar layanan..." />
      </div>
    );
  }

  if (error && !data) {
    return (
      <div className="page">
        <Card title="Layanan sistem">
          <ErrorState message={error} onRetry={() => void load()} />
        </Card>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page__intro">
        <div>
          <h2>Layanan sistem</h2>
          <p>Kelola unit systemd: mulai, hentikan, mulai ulang, serta lihat status dan log terakhir.</p>
        </div>
        <div className="page__intro-actions">
          <Button variant="ghost" onClick={() => void load()} icon={<IconRefresh width={16} height={16} />}>
            Muat ulang
          </Button>
        </div>
      </div>

      <Card
        title="Daftar layanan"
        subtitle={`${data?.total ?? 0} unit terpasang · ${data?.running ?? 0} berjalan${data?.failed ? ` · ${data.failed} gagal` : ""}`}
        padded={false}
        actions={
          <label className="ops-toggle">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
            <span>Otomatis 5 detik</span>
          </label>
        }
      >
        <div className="ops-toolbar ops-toolbar--padded">
          <span className="ops-search">
            <IconSearch width={15} height={15} />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Cari nama atau keterangan layanan..."
              aria-label="Cari layanan"
              spellCheck={false}
            />
          </span>
          <div className="ops-filters" role="group" aria-label="Filter status">
            {STATE_FILTERS.map((f) => (
              <button
                key={f.key}
                type="button"
                className={`ops-chip ${filter === f.key ? "ops-chip--active" : ""}`}
                onClick={() => setFilter(f.key)}
              >
                {f.label}
                {f.key !== "all" && data ? (
                  <span className="ops-chip__count">
                    {(data.units ?? []).filter((u) => matchesFilter(u, f.key)).length}
                  </span>
                ) : null}
              </button>
            ))}
          </div>
        </div>

        {units.length === 0 ? (
          <EmptyState
            icon={<IconCog width={22} height={22} />}
            title="Tidak ada layanan yang cocok"
            description="Ubah kata kunci atau filter status."
          />
        ) : (
          <div className="ops-table">
            <div className="ops-head" style={{ gridTemplateColumns: SERVICES_COLS }}>
              <span>Layanan</span>
              <span>Status</span>
              <span>Boot</span>
              <span className="is-num">Aksi</span>
            </div>
            {units.map((unit) => {
              const state = unitState(unit);
              const busy = busyUnit === unit.name;
              return (
                <div className="ops-row" key={unit.name} style={{ gridTemplateColumns: SERVICES_COLS }}>
                  <button
                    type="button"
                    className="ops-row__primary"
                    onClick={() => setDetailUnit(unit.name)}
                    title="Lihat detail"
                  >
                    <span className="ops-row__title">{unit.name}</span>
                    <span className="ops-row__sub">{unit.description || "—"}</span>
                  </button>
                  <span className="ops-row__badges">
                    <Badge tone={state.tone}>{state.label}</Badge>
                  </span>
                  <span className={`ops-tag ops-tag--${unit.unitFile || "unknown"}`}>
                    {unit.unitFile || "—"}
                  </span>
                  <span className="ops-row__actions">
                    {unit.active === "active" ? (
                      <>
                        <IconButton label="Mulai ulang" onClick={() => setPending({ unit: unit.name, action: "restart" })} disabled={busy}>
                          <IconRefresh width={15} height={15} />
                        </IconButton>
                        <IconButton label="Hentikan" onClick={() => setPending({ unit: unit.name, action: "stop" })} disabled={busy}>
                          <IconStop width={15} height={15} />
                        </IconButton>
                      </>
                    ) : (
                      <IconButton
                        label="Mulai"
                        onClick={() => void runAction(unit.name, "start")}
                        disabled={busy || unit.load === "not-found"}
                      >
                        <IconPlay width={15} height={15} />
                      </IconButton>
                    )}
                    <Button variant="ghost" size="sm" onClick={() => setDetailUnit(unit.name)}>
                      Detail
                    </Button>
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </Card>

      <ConfirmDialog
        open={pending !== null}
        title={pending ? `${serviceActionLabel(pending.action)} layanan` : ""}
        message={
          pending
            ? pending.action === "stop"
              ? `Hentikan ${pending.unit}? Layanan yang bergantung padanya bisa ikut terdampak.`
              : `Mulai ulang ${pending.unit}? Koneksi yang sedang berjalan akan terputus sesaat.`
            : ""
        }
        confirmLabel={pending ? serviceActionLabel(pending.action) : "Lanjut"}
        destructive={pending?.action === "stop"}
        busy={busyUnit !== ""}
        onCancel={() => setPending(null)}
        onConfirm={async () => {
          if (!pending) return;
          const { unit, action } = pending;
          setPending(null);
          await runAction(unit, action);
        }}
      />

      {detailUnit && (
        <ServiceDetailModal
          unit={detailUnit}
          onClose={() => setDetailUnit(null)}
          onAction={async (action) => {
            await runAction(detailUnit, action);
          }}
          busy={busyUnit === detailUnit}
        />
      )}
    </div>
  );
}

const SERVICES_COLS = "minmax(0, 2.4fr) 260px 90px 190px";

function ServiceDetailModal({
  unit,
  onClose,
  onAction,
  busy,
}: {
  unit: string;
  onClose: () => void;
  onAction: (action: string) => void | Promise<void>;
  busy: boolean;
}) {
  const [detail, setDetail] = useState<ServiceDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setDetail(await api.opsServiceDetail(unit));
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal memuat detail layanan.");
    } finally {
      setLoading(false);
    }
  }, [unit]);

  useEffect(() => {
    void load();
  }, [load]);

  const props = detail?.properties ?? {};
  const rows: { label: string; value: string }[] = detail
    ? [
        { label: "Status", value: `${props.ActiveState ?? "-"} (${props.SubState ?? "-"})` },
        { label: "Saat boot", value: props.UnitFileState || "-" },
        { label: "Berkas unit", value: props.FragmentPath || "-" },
        { label: "PID utama", value: props.MainPID && props.MainPID !== "0" ? props.MainPID : "-" },
        {
          label: "Memori",
          value:
            detail.unit.memoryBytes && detail.unit.memoryBytes > 0
              ? formatBytes(detail.unit.memoryBytes)
              : "-",
        },
        { label: "Mulai", value: props.ExecMainStartTimestamp || "-" },
        { label: "Jalan ulang", value: `${props.Restart || "-"} · ${props.NRestarts || "0"}x` },
      ]
    : [];

  return (
    <Modal
      open
      title={unit}
      size="lg"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={() => void load()} disabled={busy}>
            Muat ulang data
          </Button>
          <Button variant="secondary" onClick={() => void onAction("restart")} disabled={busy}>
            Mulai ulang
          </Button>
          <Button variant="danger" onClick={() => void onAction("stop")} disabled={busy}>
            Hentikan
          </Button>
        </>
      }
    >
      {loading && !detail ? (
        <Spinner label="Memuat detail..." />
      ) : error && !detail ? (
        <ErrorState message={error} onRetry={() => void load()} />
      ) : (
        <div className="ops-detail">
          {detail?.unit.description && <p className="muted">{detail.unit.description}</p>}
          <dl className="detail-list">
            {rows.map((row) => (
              <div key={row.label}>
                <dt>{row.label}</dt>
                <dd className="mono-wrap">{row.value}</dd>
              </div>
            ))}
          </dl>

          <h3 className="ops-detail__title">Log terakhir</h3>
          {detail?.journal && detail.journal.length > 0 ? (
            <div className="log-view log-view--embedded">
              {detail.journal.map((entry, index) => (
                <div className="log-line" key={`${entry.time}-${index}`}>
                  <span className="log-line__time">{formatDateTime(entry.time)}</span>
                  <span className={`log-line__prio log-line__prio--${entry.priority}`}>
                    {priorityLabel(entry.priority)}
                  </span>
                  <span className="log-line__msg">{entry.message}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="muted">Belum ada entri log untuk unit ini.</p>
          )}
        </div>
      )}
    </Modal>
  );
}
