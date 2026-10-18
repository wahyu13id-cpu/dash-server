// Presentational building blocks for the host-monitoring dashboard. All values
// come from GET /api/system; these components are pure and stateless.
import type { ReactNode } from "react";
import type { DiskInfo, NetworkInfo, ProcessInfo } from "../lib/types";
import { formatBytes } from "../lib/format";

type Tone = "ok" | "warn" | "danger";

export function toneForPercent(percent: number): Tone {
  if (percent >= 90) return "danger";
  if (percent >= 70) return "warn";
  return "ok";
}

export function UsageBar({ percent, tone }: { percent: number; tone?: Tone }) {
  const clamped = Math.max(0, Math.min(100, percent));
  const t = tone ?? toneForPercent(clamped);
  return (
    <div
      className="usage-bar"
      role="progressbar"
      aria-valuenow={Math.round(clamped)}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <span className={`usage-bar__fill usage-bar__fill--${t}`} style={{ width: `${clamped}%` }} />
    </div>
  );
}

// Sparkline draws a small area chart from a 0-100 series. The viewBox is
// stretched with preserveAspectRatio="none", so the stroke uses
// non-scaling-stroke to keep a constant width.
export function Sparkline({ values, height = 44 }: { values: number[]; height?: number }) {
  if (!values || values.length < 2) {
    return <div className="sparkline sparkline--empty" style={{ height }} aria-hidden="true" />;
  }
  const top = Math.max(10, ...values);
  const step = 100 / (values.length - 1);
  const points = values.map((v, i) => {
    const x = i * step;
    const y = 100 - (Math.max(0, Math.min(top, v)) / top) * 100;
    return [x, y] as const;
  });
  const line = points.map(([x, y]) => `${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
  const area = `0,100 ${line} 100,100`;

  return (
    <svg
      className="sparkline"
      viewBox="0 0 100 100"
      preserveAspectRatio="none"
      style={{ height }}
      aria-hidden="true"
    >
      <polygon className="sparkline__area" points={area} />
      <polyline className="sparkline__line" points={line} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function MetricCard({
  icon,
  label,
  value,
  sub,
  percent,
  tone,
  children,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  sub?: ReactNode;
  percent?: number;
  tone?: Tone;
  children?: ReactNode;
}) {
  return (
    <div className="metric-card">
      <div className="metric-card__head">
        <span className="metric-card__icon">{icon}</span>
        <span className="metric-card__label">{label}</span>
      </div>
      <div className="metric-card__value">{value}</div>
      {percent !== undefined && <UsageBar percent={percent} tone={tone} />}
      {sub && <div className="metric-card__sub">{sub}</div>}
      {children}
    </div>
  );
}

// CoreStrip renders one tiny bar per logical CPU, coloured by load.
export function CoreStrip({ perCore }: { perCore: number[] }) {
  if (!perCore || perCore.length === 0) return null;
  return (
    <div className="core-strip" aria-hidden="true">
      {perCore.map((v, i) => {
        const clamped = Math.max(0, Math.min(100, v));
        return (
          <span className="core-strip__cell" key={i} title={`Core ${i}: ${v.toFixed(1)}%`}>
            <span
              className={`core-strip__bar core-strip__bar--${toneForPercent(clamped)}`}
              style={{ height: `${Math.max(6, clamped)}%` }}
            />
          </span>
        );
      })}
    </div>
  );
}

export function ProcessTable({ rows, metric }: { rows: ProcessInfo[]; metric: "cpu" | "memory" }) {
  if (!rows || rows.length === 0) {
    return <p className="muted">Tidak ada data proses.</p>;
  }
  return (
    <div className="data-table">
      <div className="data-table__head">
        <span>PID</span>
        <span>Proses</span>
        <span className="is-num">CPU</span>
        <span className="is-num">MEM</span>
        <span className="is-num">RSS</span>
      </div>
      {rows.map((p) => (
        <div className="data-table__row" key={`${metric}-${p.pid}`}>
          <span className="data-table__mono">{p.pid}</span>
          <span className="data-table__name" title={p.command || p.name}>
            {p.name}
          </span>
          <span className={`data-table__num ${p.cpuPercent >= 50 ? "is-hot" : ""}`}>
            {p.cpuPercent.toFixed(1)}%
          </span>
          <span className="data-table__num">{p.memPercent.toFixed(1)}%</span>
          <span className="data-table__num data-table__num--muted">{formatBytes(p.rssBytes)}</span>
        </div>
      ))}
    </div>
  );
}

export function DiskList({ disks }: { disks: DiskInfo[] }) {
  if (!disks || disks.length === 0) {
    return <p className="muted">Tidak ada filesystem terdeteksi.</p>;
  }
  return (
    <ul className="disk-list">
      {disks.map((d) => (
        <li className="disk-item" key={`${d.device}-${d.mount}`}>
          <div className="disk-item__top">
            <span className="disk-item__mount" title={d.device}>
              {d.mount}
            </span>
            <span className="disk-item__meta">
              {formatBytes(d.usedBytes)} / {formatBytes(d.totalBytes)} · {d.usedPercent.toFixed(0)}%
            </span>
          </div>
          <UsageBar percent={d.usedPercent} />
          <div className="disk-item__foot">
            {d.device} <span className="tag">{d.fsType}</span>
          </div>
        </li>
      ))}
    </ul>
  );
}

export function NetworkList({ network }: { network: NetworkInfo }) {
  if (!network.interfaces || network.interfaces.length === 0) {
    return <p className="muted">Tidak ada antarmuka jaringan.</p>;
  }
  return (
    <ul className="net-list">
      {network.interfaces.map((i) => (
        <li className="net-item" key={i.name}>
          <span className="net-item__name">{i.name}</span>
          <span className="net-item__rate is-down" title="Unduh (masuk)">
            ↓ {formatRate(i.rxBytesPerSec)}
          </span>
          <span className="net-item__rate is-up" title="Unggah (keluar)">
            ↑ {formatRate(i.txBytesPerSec)}
          </span>
          <span className="net-item__total" title="Total sejak boot">
            {formatBytes(i.rxBytes)} / {formatBytes(i.txBytes)}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function formatRate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 B/s";
  return `${formatBytes(bytesPerSec)}/s`;
}
