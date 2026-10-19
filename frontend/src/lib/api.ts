// Thin fetch wrapper around the REST API. It attaches the CSRF token from the
// double-submit cookie and normalizes error responses into ApiError.

import type {
  AuditEntry,
  FileContent,
  FileEntry,
  FileListing,
  FileLog,
  FileStat,
  Health,
  LoginResponse,
  LogEntry,
  LogSourceList,
  OpsAvailability,
  OpsJob,
  PackageStatus,
  ProcessList,
  ProcessSort,
  ProfileInfo,
  RecoveryCodesResponse,
  ServiceDetail,
  ServiceList,
  SshEffective,
  SshHost,
  SshHostInput,
  SshHostKeyInfo,
  SshQuickAdd,
  SshSecretKind,
  SshState,
  SshTestResult,
  Stats,
  SystemResponse,
  TerminalSession,
  TerminalSessionsResponse,
  TerminalStats,
  TotpSetup,
  TotpStatus,
  UpdateProfileResult,
  User,
} from "./types";

export class ApiError extends Error {
  code: string;
  status: number;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

function readCookie(name: string): string {
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
  return match ? decodeURIComponent(match[1]) : "";
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  query?: Record<string, string | undefined>;
  signal?: AbortSignal;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const url = new URL(path, window.location.origin);
  if (opts.query) {
    for (const [key, value] of Object.entries(opts.query)) {
      if (value !== undefined && value !== "") {
        url.searchParams.set(key, value);
      }
    }
  }

  const headers: Record<string, string> = { Accept: "application/json" };
  const init: RequestInit = {
    method: opts.method ?? "GET",
    credentials: "same-origin",
    headers,
    signal: opts.signal,
  };

  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }

  const method = (opts.method ?? "GET").toUpperCase();
  if (method !== "GET" && method !== "HEAD") {
    const csrf = readCookie("dash_csrf");
    if (csrf) {
      headers["X-CSRF-Token"] = csrf;
    }
  }

  let response: Response;
  try {
    response = await fetch(url.toString(), init);
  } catch (err) {
    if ((err as Error).name === "AbortError") {
      throw err;
    }
    throw new ApiError(0, "network_error", "Tidak dapat menghubungi server.");
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  let payload: unknown = undefined;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = undefined;
    }
  }

  if (!response.ok) {
    const body = payload as { error?: { code?: string; message?: string } } | undefined;
    throw new ApiError(
      response.status,
      body?.error?.code ?? "unknown",
      body?.error?.message ?? `Permintaan gagal (${response.status}).`,
    );
  }

  return payload as T;
}

export interface UploadTask {
  promise: Promise<FileEntry>;
  abort: () => void;
}

export interface UploadOptions {
  overwrite?: boolean;
  onProgress?: (loaded: number, total: number) => void;
}

/**
 * fileDownloadURL returns the direct download link for a file. Downloads are
 * plain navigations so the browser streams them to disk itself (with its own
 * progress and resume support) instead of buffering the whole file in memory.
 */
export function fileDownloadURL(path: string): string {
  const url = new URL("/api/files/download", window.location.origin);
  url.searchParams.set("path", path);
  return url.toString();
}

/**
 * archiveDownloadURL is the download link for one or more paths packed as a
 * ZIP. Like a plain download it is used as a plain navigation, so the archive is
 * streamed to disk while the server walks the tree.
 */
export function archiveDownloadURL(paths: string[], name?: string): string {
  const url = new URL("/api/files/zip", window.location.origin);
  for (const path of paths) url.searchParams.append("path", path);
  if (name) url.searchParams.set("name", name);
  return url.toString();
}

/**
 * uploadFile stores one file in a directory.
 *
 * Uploads go through XMLHttpRequest because fetch cannot report upload
 * progress, and the body is the raw file (no multipart wrapper) so the server
 * can stream it straight to disk. Files are uploaded one at a time: each one
 * gets its own progress bar, its own error and its own overwrite prompt.
 */
export function uploadFile(path: string, file: File, options: UploadOptions = {}): UploadTask {
  const url = new URL("/api/files/upload", window.location.origin);
  url.searchParams.set("path", path);
  url.searchParams.set("name", file.name);
  if (options.overwrite) url.searchParams.set("overwrite", "1");

  const xhr = new XMLHttpRequest();
  const promise = new Promise<FileEntry>((resolve, reject) => {
    xhr.open("POST", url.toString());
    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    const csrf = readCookie("dash_csrf");
    if (csrf) xhr.setRequestHeader("X-CSRF-Token", csrf);

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) options.onProgress?.(event.loaded, event.total);
    };

    xhr.onload = () => {
      let payload: unknown;
      try {
        payload = JSON.parse(xhr.responseText);
      } catch {
        payload = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(payload as FileEntry);
        return;
      }
      const body = payload as { error?: { code?: string; message?: string } } | undefined;
      reject(
        new ApiError(
          xhr.status,
          body?.error?.code ?? "unknown",
          body?.error?.message ?? `Unggahan gagal (${xhr.status}).`,
        ),
      );
    };

    xhr.onerror = () => reject(new ApiError(0, "network_error", "Koneksi unggahan terputus."));
    xhr.onabort = () => reject(new DOMException("Unggahan dibatalkan.", "AbortError"));
    xhr.send(file);
  });

  return { promise, abort: () => xhr.abort() };
}

export const api = {
  health: () => request<Health>("/api/health"),

  login: (username: string, password: string) =>
    request<LoginResponse>("/api/auth/login", {
      method: "POST",
      body: { username, password },
    }),

  loginTotp: (challenge: string, code: string) =>
    request<LoginResponse>("/api/auth/login/totp", {
      method: "POST",
      body: { challenge, code },
    }),

  logout: () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),

  me: () => request<{ user: User }>("/api/auth/me"),

  changePassword: (currentPassword: string, newPassword: string) =>
    request<{ ok: boolean }>("/api/auth/password", {
      method: "POST",
      body: { currentPassword, newPassword },
    }),

  /** Account, session and 2FA state for the profile page. */
  profile: () => request<ProfileInfo>("/api/auth/profile"),

  /**
   * Self-service profile edit. Only the fields that are present are touched;
   * the current password is always required.
   */
  updateProfile: (payload: {
    currentPassword: string;
    username?: string;
    displayName?: string;
    newPassword?: string;
  }) => request<UpdateProfileResult>("/api/auth/profile", { method: "PATCH", body: payload }),

  totpStatus: () => request<TotpStatus>("/api/auth/totp"),

  totpSetup: () => request<TotpSetup>("/api/auth/totp/setup", { method: "POST" }),

  totpActivate: (code: string) =>
    request<RecoveryCodesResponse>("/api/auth/totp/activate", {
      method: "POST",
      body: { code },
    }),

  totpDisable: (password: string, code: string) =>
    request<{ ok: boolean }>("/api/auth/totp/disable", {
      method: "POST",
      body: { password, code },
    }),

  totpRegenerateRecovery: (password: string, code: string) =>
    request<RecoveryCodesResponse>("/api/auth/totp/recovery/regenerate", {
      method: "POST",
      body: { password, code },
    }),

  stats: () => request<Stats>("/api/stats"),

  system: (signal?: AbortSignal) => request<SystemResponse>("/api/system", { signal }),

  audit: () => request<{ entries: AuditEntry[] }>("/api/audit"),

  listFiles: (path: string, signal?: AbortSignal) =>
    request<FileListing>("/api/files", { query: { path }, signal }),

  readFile: (path: string, signal?: AbortSignal) =>
    request<FileContent>("/api/files/content", { query: { path }, signal }),

  writeFile: (path: string, content: string) =>
    request<FileContent>("/api/files/content", {
      method: "PUT",
      query: { path },
      body: { content },
    }),

  createEntry: (path: string, kind: "file" | "folder", overwrite = false) =>
    request<FileEntry>("/api/files", {
      method: "POST",
      body: { path, kind, overwrite },
    }),

  renameEntry: (from: string, to: string) =>
    request<FileEntry>("/api/files", {
      method: "PATCH",
      body: { from, to },
    }),

  deleteEntry: (path: string) =>
    request<{ ok: boolean; path: string }>("/api/files", {
      method: "DELETE",
      query: { path },
    }),

  copyEntry: (from: string, to: string, overwrite = false) =>
    request<FileEntry>("/api/files/copy", {
      method: "POST",
      body: { from, to, overwrite },
    }),

  moveEntry: (from: string, to: string) =>
    request<FileEntry>("/api/files/move", {
      method: "POST",
      body: { from, to },
    }),

  chmodEntry: (path: string, mode: string) =>
    request<FileEntry>("/api/files/mode", {
      method: "PATCH",
      body: { path, mode },
    }),

  statEntry: (path: string, signal?: AbortSignal) =>
    request<FileStat>("/api/files/stat", { query: { path }, signal }),

  unzipEntry: (path: string, dest: string, overwrite = false) =>
    request<{ ok: boolean; entries: number; dest: string }>("/api/files/unzip", {
      method: "POST",
      body: { path, dest, overwrite },
    }),

  listTerminalSessions: () => request<TerminalSessionsResponse>("/api/terminal/sessions"),

  createTerminalSession: (
    kind: "shell" | "ssh",
    target: string,
    cols: number,
    rows: number,
    dir = "",
  ) =>
    request<TerminalSession>("/api/terminal/sessions", {
      method: "POST",
      body: { kind, target, cols, rows, dir },
    }),

  deleteTerminalSession: (id: string) =>
    request<{ ok: boolean; id: string }>(`/api/terminal/sessions/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  /** terminalStats feeds the status strip above a session (local or remote host). */
  terminalStats: (id: string, signal?: AbortSignal) =>
    request<TerminalStats>(`/api/terminal/sessions/${encodeURIComponent(id)}/stats`, { signal }),

  // --- SSH connection manager -------------------------------------------

  sshState: (signal?: AbortSignal) => request<SshState>("/api/ssh/state", { signal }),

  sshParse: (command: string) =>
    request<SshQuickAdd>("/api/ssh/parse", { method: "POST", body: { command } }),

  sshCreateHost: (input: SshHostInput) =>
    request<SshHost>("/api/ssh/hosts", { method: "POST", body: input }),

  sshGetHost: (alias: string) =>
    request<{ host: SshHost; hasPassword: boolean; hasPassphrase: boolean }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}`,
    ),

  sshUpdateHost: (alias: string, input: SshHostInput) =>
    request<SshHost>(`/api/ssh/hosts/${encodeURIComponent(alias)}`, {
      method: "PATCH",
      body: input,
    }),

  sshDeleteHost: (alias: string) =>
    request<{ deleted: string }>(`/api/ssh/hosts/${encodeURIComponent(alias)}`, {
      method: "DELETE",
    }),

  sshSetFavorite: (alias: string, favorite: boolean) =>
    request<{ alias: string; favorite: boolean }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/favorite`,
      { method: "POST", body: { favorite } },
    ),

  sshSetNote: (alias: string, note: string) =>
    request<{ alias: string }>(`/api/ssh/hosts/${encodeURIComponent(alias)}/note`, {
      method: "PUT",
      body: { note },
    }),

  sshEffective: (alias: string) =>
    request<SshEffective>(`/api/ssh/hosts/${encodeURIComponent(alias)}/effective`),

  sshTest: (alias: string) =>
    request<SshTestResult>(`/api/ssh/hosts/${encodeURIComponent(alias)}/test`, {
      method: "POST",
    }),

  // Secrets are write-only: the value is never returned by any endpoint.
  sshSetSecret: (alias: string, kind: SshSecretKind, value: string) =>
    request<{ alias: string; kind: string; stored: boolean }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/secret`,
      { method: "PUT", body: { kind, value } },
    ),

  sshClearSecret: (alias: string, kind?: SshSecretKind) =>
    request<{ alias: string; cleared: boolean }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/secret`,
      { method: "DELETE", query: { kind } },
    ),

  sshHostKey: (alias: string) =>
    request<SshHostKeyInfo>(`/api/ssh/hosts/${encodeURIComponent(alias)}/hostkey`),

  sshScanHostKey: (alias: string) =>
    request<{ alias: string; fingerprints: SshHostKeyFPList }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/hostkey/scan`,
      { method: "POST" },
    ),

  sshTrustHostKey: (alias: string) =>
    request<{ alias: string; trusted: number }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/hostkey/trust`,
      { method: "POST" },
    ),

  sshForgetHostKey: (alias: string) =>
    request<{ alias: string; removed: boolean }>(
      `/api/ssh/hosts/${encodeURIComponent(alias)}/hostkey`,
      { method: "DELETE" },
    ),

  // --- host operations: services, processes, logs, packages ---------------

  /** Which operations this host supports (drives the navigation and pages). */
  opsAvailability: () => request<OpsAvailability>("/api/ops/availability"),

  opsServices: () => request<ServiceList>("/api/ops/services"),

  opsServiceDetail: (name: string) =>
    request<ServiceDetail>(`/api/ops/services/${encodeURIComponent(name)}`),

  opsServiceAction: (name: string, action: string) =>
    request<{ action: string; unit: string; output: string }>(
      `/api/ops/services/${encodeURIComponent(name)}/action`,
      { method: "POST", body: { action } },
    ),

  opsProcesses: (params: { sort?: ProcessSort; q?: string; user?: string; limit?: number } = {}) =>
    request<ProcessList>("/api/ops/processes", {
      query: {
        sort: params.sort,
        q: params.q,
        user: params.user,
        limit: params.limit === undefined ? undefined : String(params.limit),
      },
    }),

  opsProcessSignal: (pid: number, signal: string) =>
    request<{ pid: number; signal: string }>(`/api/ops/processes/${pid}/signal`, {
      method: "POST",
      body: { signal },
    }),

  opsProcessNice: (pid: number, nice: number) =>
    request<{ pid: number; nice: number }>(`/api/ops/processes/${pid}/nice`, {
      method: "POST",
      body: { nice },
    }),

  opsLogSources: () => request<LogSourceList>("/api/ops/logs/sources"),

  opsJournal: (params: {
    unit?: string;
    priority?: string;
    lines?: number;
    since?: string;
    search?: string;
  }) =>
    request<{ entries: LogEntry[] | null }>("/api/ops/logs/journal", {
      query: {
        unit: params.unit,
        priority: params.priority,
        lines: params.lines === undefined ? undefined : String(params.lines),
        since: params.since,
        search: params.search,
      },
    }),

  opsLogFile: (params: { path: string; lines?: number; search?: string }) =>
    request<FileLog>("/api/ops/logs/file", {
      query: {
        path: params.path,
        lines: params.lines === undefined ? undefined : String(params.lines),
        search: params.search,
      },
    }),

  opsPackages: (refresh = false) =>
    request<PackageStatus>("/api/ops/packages", { query: { refresh: refresh ? "1" : undefined } }),

  opsRefreshPackages: () => request<OpsJob>("/api/ops/packages/refresh", { method: "POST" }),

  opsUpgradePackages: (dist = false) =>
    request<OpsJob>("/api/ops/packages/upgrade", { method: "POST", body: { dist } }),

  opsJob: (id: string) => request<OpsJob>(`/api/ops/jobs/${encodeURIComponent(id)}`),
};

type SshHostKeyFPList = SshHostKeyInfo["fingerprints"];

// terminalSocketURL builds the websocket URL for a session, matching the
// current page's protocol.
export function terminalSocketURL(id: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/terminal/sessions/${encodeURIComponent(id)}/stream`;
}
