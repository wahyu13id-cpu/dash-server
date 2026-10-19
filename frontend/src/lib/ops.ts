// Shared helpers for the host-operations pages (services, processes, logs,
// packages). They are presentation-only: no data fetching lives here.

/** Human label for a journald priority (0 = emerg … 7 = debug). */
export function priorityLabel(priority: number): string {
  switch (priority) {
    case 0:
      return "emerg";
    case 1:
      return "alert";
    case 2:
      return "crit";
    case 3:
      return "err";
    case 4:
      return "warn";
    case 5:
      return "notice";
    case 6:
      return "info";
    default:
      return "debug";
  }
}

/** Badge tone for a journald priority. */
export function priorityTone(priority: number): "danger" | "warning" | "info" | "neutral" {
  if (priority <= 3) return "danger";
  if (priority === 4) return "warning";
  if (priority <= 6) return "info";
  return "neutral";
}

/** Indonesian label for a systemctl verb. */
export function serviceActionLabel(action: string): string {
  switch (action) {
    case "start":
      return "Mulai";
    case "stop":
      return "Hentikan";
    case "restart":
      return "Mulai ulang";
    case "reload":
      return "Muat ulang";
    case "try-restart":
      return "Mulai ulang bila berjalan";
    case "enable":
      return "Aktifkan saat boot";
    case "disable":
      return "Matikan saat boot";
    case "mask":
      return "Mask";
    case "unmask":
      return "Unmask";
    default:
      return action;
  }
}

/** First non-empty line of a command's output, trimmed for a toast. */
export function firstLine(text: string): string {
  const line = (text ?? "").split("\n").find((l) => l.trim() !== "");
  return (line ?? "selesai").trim().slice(0, 160);
}

/** Letter → readable process state (R, S, D, Z, T, I). */
export function processStateLabel(state: string): string {
  switch (state) {
    case "R":
      return "jalan";
    case "S":
      return "tidur";
    case "D":
      return "disk";
    case "Z":
      return "zombie";
    case "T":
      return "stop";
    case "t":
      return "trace";
    case "I":
      return "idle";
    default:
      return state || "-";
  }
}

/** Priorities offered by the log page filter. */
export const JOURNAL_PRIORITIES = [
  { value: "", label: "Semua level" },
  { value: "err", label: "Error ke atas" },
  { value: "warning", label: "Peringatan ke atas" },
  { value: "notice", label: "Notice ke atas" },
  { value: "info", label: "Info ke atas" },
  { value: "debug", label: "Debug (semua)" },
];

/** Time windows offered by the log page filter. */
export const JOURNAL_WINDOWS = [
  { value: "", label: "Semua waktu" },
  { value: "15m", label: "15 menit terakhir" },
  { value: "1h", label: "1 jam terakhir" },
  { value: "6h", label: "6 jam terakhir" },
  { value: "24h", label: "24 jam terakhir" },
  { value: "7d", label: "7 hari terakhir" },
  { value: "boot", label: "Sejak boot terakhir" },
];
