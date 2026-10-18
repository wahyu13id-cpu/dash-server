import { useEffect, useState } from "react";

import { api } from "../lib/api";
import { formatBytes, formatDuration, formatRate } from "../lib/format";
import type { TerminalStats } from "../lib/types";

/**
 * How often the strip refreshes while everything works. The server caches every
 * host for two seconds and reuses one SSH connection per host, so a short
 * interval costs almost nothing — and only the visible tab polls.
 */
const POLL_MS = 3000;

/** Poll less often while the browser tab is in the background. */
const HIDDEN_POLL_MS = 15000;

/**
 * A remote probe that fails usually failed to *authenticate*, and a rejected
 * login is a rejected login for the remote host's fail2ban too. So a failure is
 * retried once, slowly, and then the strip waits for the user instead of
 * hammering the host every few seconds.
 */
const FAILURE_RETRY_MS = 60000;
const MAX_AUTO_RETRIES = 1;

type Tone = "ok" | "warn" | "crit";

function toneOf(percent: number): Tone {
  if (percent >= 85) return "crit";
  if (percent >= 60) return "warn";
  return "ok";
}

function percentOf(used: number | undefined, total: number | undefined): number | null {
  if (total === undefined || total <= 0 || used === undefined) return null;
  return (used / total) * 100;
}

function usageText(used: number | undefined, total: number | undefined): string {
  if (!total) return "-";
  return `${formatBytes(used ?? 0)} / ${formatBytes(total)}`;
}

/**
 * Raw ssh wording is precise but rarely actionable, so the causes a user can do
 * something about are spelled out.
 */
function friendlyMessage(raw: string): string {
  if (!raw) return "Metrik host tidak tersedia.";
  if (/Permission denied|publickey,password/i.test(raw)) {
    return "Metrik remote butuh kredensial: simpan password SSH host ini (atau pasang kunci) supaya angkanya bisa dibaca.";
  }
  if (/Connection refused|timed out|No route to host|Could not resolve/i.test(raw)) {
    return `Host tidak bisa dihubungi — ${raw}`;
  }
  return raw;
}

interface TerminalStatsStripProps {
  /** Empty until the session has been created; the strip shows a placeholder. */
  sessionId: string;
  kind: string;
  target: string;
}

/**
 * TerminalStatsStrip is the one-line host readout above a terminal, in the spirit
 * of the MobaXterm status bar: hostname, CPU, memory, bandwidth and disk of
 * whichever machine the session is connected to (this server for a local shell,
 * the remote host for SSH).
 *
 * It is display-only and never touches the terminal, so it cannot interfere with
 * a running command.
 */
export function TerminalStatsStrip({ sessionId, kind, target }: TerminalStatsStripProps) {
  const [stats, setStats] = useState<TerminalStats | null>(null);
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    setStats(null);
    setError("");
    setPaused(false);
    if (!sessionId) return;

    let disposed = false;
    let timer = 0;
    let pending: AbortController | null = null;
    let failures = 0;

    const schedule = (ms: number) => {
      if (disposed) return;
      timer = window.setTimeout(tick, document.hidden ? Math.max(ms, HIDDEN_POLL_MS) : ms);
    };

    const tick = async () => {
      if (disposed) return;
      pending = new AbortController();
      let healthy = false;
      let next: TerminalStats | null = null;
      try {
        next = await api.terminalStats(sessionId, pending.signal);
        healthy = next.available;
      } catch (err) {
        if (disposed) return;
        if (err instanceof DOMException && err.name === "AbortError") return;
        setError(err instanceof Error ? err.message : "Gagal memuat metrik host.");
      } finally {
        pending = null;
      }
      if (disposed) return;

      if (next) {
        setStats(next);
        if (next.available) setError("");
      }
      if (healthy) {
        failures = 0;
        schedule(POLL_MS);
        return;
      }
      failures += 1;
      if (failures > MAX_AUTO_RETRIES) {
        setPaused(true);
        return;
      }
      schedule(FAILURE_RETRY_MS);
    };

    void tick();
    return () => {
      disposed = true;
      window.clearTimeout(timer);
      pending?.abort();
    };
  }, [sessionId, attempt]);

  const host = (stats?.available ? stats.host : "") || target || "host";
  const cpu = stats?.cpuPercent;
  const memPercent = percentOf(stats?.memUsedBytes, stats?.memTotalBytes);
  const diskPercent = percentOf(stats?.diskUsedBytes, stats?.diskTotalBytes);

  const note =
    error ||
    (stats ? friendlyMessage(stats.message ?? "") : !sessionId ? "Menyiapkan sesi\u2026" : "Memuat metrik host\u2026");

  const tooltip = [
    stats?.available
      ? stats.scope === "remote"
        ? `Host remote ${stats.host}`
        : `Host lokal ${stats.host}`
      : `Host ${host}`,
    stats?.available && cpu !== undefined ? `CPU ${cpu.toFixed(1)}%` : "",
    stats?.available && stats.memTotalBytes
      ? `Memori ${usageText(stats.memUsedBytes, stats.memTotalBytes)}`
      : "",
    stats?.available && stats.swapTotalBytes
      ? `Swap ${usageText(stats.swapUsedBytes, stats.swapTotalBytes)}`
      : "",
    stats?.available
      ? `Jaringan \u2193 ${formatRate(stats.netRxBytesPerSec)} \u2191 ${formatRate(stats.netTxBytesPerSec)}`
      : "",
    stats?.available && stats.diskTotalBytes
      ? `Disk ${usageText(stats.diskUsedBytes, stats.diskTotalBytes)}`
      : "",
    stats?.available && stats.load1 !== undefined ? `Load ${stats.load1.toFixed(2)}` : "",
    stats?.available && stats.uptimeSec ? `Uptime ${formatDuration(stats.uptimeSec)}` : "",
    stats?.collectedAt ? `Sampel ${new Date(stats.collectedAt).toLocaleTimeString()}` : "",
  ]
    .filter(Boolean)
    .join(" \u00b7 ");

  return (
    <div className="term-stats" title={tooltip}>
      <span className={`term-stats__badge term-stats__badge--${kind === "ssh" ? "ssh" : "local"}`}>
        {kind === "ssh" ? "SSH" : "LOKAL"}
      </span>
      <span className="term-stats__item term-stats__host">
        <span className="term-stats__key">host</span>
        <span className="term-stats__value">{host}</span>
      </span>

      {stats?.available ? (
        <>
          <span className="term-stats__item">
            <span className="term-stats__key">cpu</span>
            <span
              className={`term-stats__value term-stats__value--${
                cpu === undefined ? "muted" : toneOf(cpu)
              }`}
            >
              {cpu === undefined ? "\u2026" : `${cpu.toFixed(cpu >= 10 ? 0 : 1)}%`}
            </span>
            {stats.cpuCount ? <span className="term-stats__hint">{stats.cpuCount} core</span> : null}
          </span>

          <span className="term-stats__item">
            <span className="term-stats__key">mem</span>
            <span
              className={`term-stats__value term-stats__value--${
                memPercent === null ? "muted" : toneOf(memPercent)
              }`}
            >
              {usageText(stats.memUsedBytes, stats.memTotalBytes)}
            </span>
            {memPercent !== null ? (
              <span className="term-stats__hint">{memPercent.toFixed(0)}%</span>
            ) : null}
          </span>

          <span className="term-stats__item">
            <span className="term-stats__key">bw</span>
            <span className="term-stats__value">
              <span className="term-stats__arrow">{"\u2193"}</span>
              {formatRate(stats.netRxBytesPerSec)}
              <span className="term-stats__arrow">{"\u2191"}</span>
              {formatRate(stats.netTxBytesPerSec)}
            </span>
          </span>

          <span className="term-stats__item">
            <span className="term-stats__key">disk</span>
            <span
              className={`term-stats__value term-stats__value--${
                diskPercent === null ? "muted" : toneOf(diskPercent)
              }`}
            >
              {usageText(stats.diskUsedBytes, stats.diskTotalBytes)}
            </span>
            {diskPercent !== null ? (
              <span className="term-stats__hint">{diskPercent.toFixed(0)}%</span>
            ) : null}
          </span>

          <span className="term-stats__item term-stats__item--wide">
            <span className="term-stats__key">load</span>
            <span className="term-stats__value">
              {stats.load1 === undefined ? "-" : stats.load1.toFixed(2)}
            </span>
          </span>

          <span className="term-stats__item term-stats__item--wide">
            <span className="term-stats__key">up</span>
            <span className="term-stats__value">
              {stats.uptimeSec ? formatDuration(stats.uptimeSec) : "-"}
            </span>
          </span>
        </>
      ) : (
        <>
          <span className="term-stats__note">{note}</span>
          {paused && (
            <button
              type="button"
              className="term-stats__retry"
              onClick={() => {
                setPaused(false);
                setAttempt((n) => n + 1);
              }}
            >
              Coba lagi
            </button>
          )}
        </>
      )}
    </div>
  );
}
