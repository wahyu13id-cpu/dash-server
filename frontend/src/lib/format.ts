export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "-";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, i);
  return `${value.toFixed(value >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatDateTime(iso: string): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function formatRelative(iso: string): string {
  if (!iso) return "-";
  const d = new Date(iso).getTime();
  if (Number.isNaN(d)) return iso;
  const diff = Date.now() - d;
  const abs = Math.abs(diff);
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (abs < minute) return "baru saja";
  if (abs < hour) return `${Math.round(abs / minute)} menit lalu`;
  if (abs < day) return `${Math.round(abs / hour)} jam lalu`;
  return `${Math.round(abs / day)} hari lalu`;
}

// formatRate renders a byte-per-second rate (network throughput, disk I/O).
export function formatRate(bytesPerSecond: number | undefined): string {
  if (bytesPerSecond === undefined || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) {
    return "-";
  }
  return `${formatBytes(bytesPerSecond)}/s`;
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "-";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (d > 0) return `${d}h ${h}j`;
  if (h > 0) return `${h}j ${m}m`;
  if (m > 0) return `${m}m ${s}d`;
  return `${s}d`;
}

// Normalize any user-typed path into a clean absolute POSIX path ("/...").
export function normalizePath(input: string): string {
  let p = (input ?? "").trim();
  if (!p) return "/";
  if (!p.startsWith("/")) p = `/${p}`;
  const parts: string[] = [];
  for (const seg of p.split("/")) {
    if (!seg || seg === ".") continue;
    if (seg === "..") {
      parts.pop();
      continue;
    }
    parts.push(seg);
  }
  return `/${parts.join("/")}`;
}

// Parent directory of an absolute path ("/" for the filesystem root).
export function parentPath(path: string): string {
  const p = normalizePath(path);
  if (p === "/") return "/";
  const idx = p.lastIndexOf("/");
  return idx <= 0 ? "/" : p.slice(0, idx);
}

// Join a directory with a child name, producing a normalized absolute path.
export function joinPath(dir: string, name: string): string {
  return normalizePath(`${dir}/${name}`);
}

export function baseName(path: string): string {
  const p = normalizePath(path);
  if (p === "/") return "/";
  const parts = p.split("/");
  return parts[parts.length - 1] || "/";
}
