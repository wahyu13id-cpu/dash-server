// Shared API types mirroring the Go backend DTOs.

export interface User {
  id: number;
  username: string;
  displayName: string;
  role: string;
  createdAt: string;
}

export interface LoginResponse {
  status: "ok" | "totp_required";
  user?: User;
  expiresAt?: string;
  /** Present when status is "totp_required"; echo it back with the code. */
  challenge?: string;
}

/** Account state shown on the profile page. */
export interface ProfileInfo {
  user: User;
  activeSessions: number;
  totpEnabled: boolean;
  recoveryRemaining: number;
  sessionCreatedAt?: string;
  sessionExpiresAt?: string;
  sessionIp?: string;
  sessionUserAgent?: string;
  sessionTtlSeconds: number;
  usernameMinLength: number;
  usernameMaxLength: number;
  passwordMinLength: number;
}

/** Result of a self-service profile update. */
export interface UpdateProfileResult {
  ok: boolean;
  user: User;
  usernameChanged: boolean;
  passwordChanged: boolean;
}

export interface TotpStatus {
  enabled: boolean;
  recoveryRemaining: number;
  issuer: string;
  account: string;
}

export interface TotpSetup {
  secret: string;
  uri: string;
  /** Inline data URI (image/png) of the enrollment QR code. */
  qrPng: string;
}

export interface RecoveryCodesResponse {
  recoveryCodes: string[];
}

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  isLink: boolean;
  size: number;
  mode: string;
  modified: string;
  editable: boolean;
}

export interface FileListing {
  path: string;
  entries: FileEntry[];
  truncated: boolean;
  totalCount: number;
}

/** FileStat is the detailed view behind the "Properti" dialog. */
export interface FileStat extends FileEntry {
  owner: string;
  group: string;
  uid: number;
  gid: number;
  links: number;
  inode: number;
  linkTarget?: string;
}

export interface FileContent {
  path: string;
  content: string;
  size: number;
  modified: string;
  truncated: boolean;
  binary: boolean;
  readOnly: boolean;
  language: string;
}

export interface TerminalSession {
  id: string;
  userId: number;
  title: string;
  shell: string;
  /** "shell" for a local PTY, "ssh" for a session running the ssh client. */
  kind: string;
  /** SSH alias for kind "ssh"; empty for a local shell. */
  target: string;
  status: string;
  cols: number;
  rows: number;
  createdAt: string;
  startedAt?: string;
  endedAt?: string;
  exitCode?: number | null;
  live: boolean;
  connections: number;
}

export interface TerminalSessionsResponse {
  sessions: TerminalSession[];
  limits: { maxGlobal: number; maxPerUser: number; live: number };
}

/**
 * TerminalStats is the status strip shown above a session: the resource usage of
 * the machine that session is connected to (this server for a local shell, the
 * remote host for SSH). `available` is false when the numbers cannot be read,
 * and `message` explains why.
 */
export interface TerminalStats {
  available: boolean;
  /** "local" for a shell on this server, "remote" for an SSH session. */
  scope: string;
  host: string;
  alias?: string;
  message?: string;
  collectedAt?: string;
  /** Absent on the first remote sample: a rate needs two probes. */
  cpuPercent?: number;
  cpuCount?: number;
  load1?: number;
  memTotalBytes?: number;
  memUsedBytes?: number;
  swapTotalBytes?: number;
  swapUsedBytes?: number;
  netRxBytesPerSec?: number;
  netTxBytesPerSec?: number;
  diskTotalBytes?: number;
  diskUsedBytes?: number;
  uptimeSec?: number;
}

export interface Stats {
  uptimeSec: number;
  goVersion: string;
  liveTerminals: number;
  maxTerminals: number;
  myTerminals: number;
  workspaceRoot: string;
  rootEntries: number;
  maxReadBytes: number;
  maxWriteBytes: number;
  maxUploadBytes: number;
  serverTime: string;
  recentActivity: number;
}

export interface HostInfo {
  hostname: string;
  kernel: string;
  arch: string;
  uptimeSec: number;
}

export interface CpuInfo {
  usagePercent: number;
  cores: number;
  model: string;
  perCore: number[];
  history: number[];
}

export interface MemoryInfo {
  totalBytes: number;
  usedBytes: number;
  availableBytes: number;
  freeBytes: number;
  cachedBytes: number;
  buffersBytes: number;
  usedPercent: number;
  swapTotalBytes: number;
  swapUsedBytes: number;
  swapUsedPercent: number;
}

export interface LoadInfo {
  load1: number;
  load5: number;
  load15: number;
  running: number;
  total: number;
}

export interface DiskInfo {
  device: string;
  mount: string;
  fsType: string;
  totalBytes: number;
  usedBytes: number;
  freeBytes: number;
  usedPercent: number;
}

export interface DisksInfo {
  filesystems: DiskInfo[];
  totalBytes: number;
  usedBytes: number;
  freeBytes: number;
  usedPercent: number;
}

export interface NetIfaceInfo {
  name: string;
  rxBytes: number;
  txBytes: number;
  rxBytesPerSec: number;
  txBytesPerSec: number;
}

export interface NetworkInfo {
  rxBytesPerSec: number;
  txBytesPerSec: number;
  rxTotalBytes: number;
  txTotalBytes: number;
  interfaces: NetIfaceInfo[];
}

export interface ProcessInfo {
  pid: number;
  name: string;
  cpuPercent: number;
  memPercent: number;
  rssBytes: number;
  threads: number;
  command: string;
}

export interface ProcessesInfo {
  total: number;
  byCpu: ProcessInfo[];
  byMemory: ProcessInfo[];
}

export interface SystemSnapshot {
  collectedAt: string;
  host: HostInfo;
  cpu: CpuInfo;
  memory: MemoryInfo;
  load: LoadInfo;
  disks: DisksInfo;
  network: NetworkInfo;
  processes: ProcessesInfo;
}

export interface SystemResponse {
  ready: boolean;
  snapshot?: SystemSnapshot;
}

export interface AuditEntry {
  id: number;
  createdAt: string;
  userId: number;
  username: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
  success: boolean;
}

export interface Health {
  status: string;
  version: string;
  time: string;
  uptimeSec: number;
}

/* --- SSH connection manager ---------------------------------------------- */

/** A single ssh_config directive, for example HostName -> web1. */
export interface SshOption {
  key: string;
  value: string;
}

export interface SshHost {
  alias: string;
  hostName: string;
  user: string;
  port: string;
  identityFile: string[] | null;
  proxyJump: string;
  comment: string;
  options: SshOption[] | null;
  file: string;
  startLine: number;
  endLine: number;
  patterns: string[] | null;
  /** True when the block lives in the writable main config. */
  writable: boolean;
  favorite: boolean;
  note?: string;
  lastConnectedAt?: string;
  connectCount: number;
  hasPassword: boolean;
  hasPassphrase: boolean;
  /** A credential is stored but cannot be decrypted (session secret changed). */
  secretBroken: boolean;
  /** The Host line defines more than one alias. */
  sharedBlock: boolean;
}

/** A block that is not a connectable host: wildcard, Match or the implied global. */
export interface SshDefaultBlock {
  kind: string;
  patterns: string[] | null;
  options: SshOption[] | null;
  file: string;
  startLine: number;
  endLine: number;
  comment?: string;
  writable: boolean;
  implied: boolean;
}

export interface SshFileInfo {
  path: string;
  primary: boolean;
  exists: boolean;
  writable: boolean;
  mode?: string;
  error?: string;
}

export interface SshKey {
  name: string;
  path: string;
  type?: string;
  fingerprint?: string;
  hasPublic: boolean;
}

export interface SshState {
  enabled: boolean;
  configPath: string;
  sshDir: string;
  exists: boolean;
  mode?: string;
  hosts: SshHost[] | null;
  defaults: SshDefaultBlock[] | null;
  files: SshFileInfo[] | null;
  warnings: string[] | null;
  keys: SshKey[] | null;
}

export interface SshHostInput {
  alias: string;
  hostName: string;
  user: string;
  port: string;
  identityFile: string[];
  proxyJump: string;
  comment: string;
  options: SshOption[];
}

export interface SshQuickAdd {
  alias: string;
  hostName: string;
  user: string;
  port: string;
  identityFile: string;
  proxyJump: string;
  warnings: string[] | null;
}

export interface SshEffective {
  options: SshOption[];
  raw: string;
}

export type SshTestStatus =
  | "ok"
  | "auth_required"
  | "host_key_unknown"
  | "unreachable"
  | "dns_error"
  | "error";

export interface SshTestResult {
  status: SshTestStatus;
  message: string;
  durationMs: number;
}

export interface SshHostKeyFP {
  type: string;
  fingerprint: string;
  /** The known_hosts line, host spec included. */
  key: string;
}

export interface SshHostKeyInfo {
  known: boolean;
  hostSpec: string;
  fingerprints: SshHostKeyFP[] | null;
}

export type SshSecretKind = "password" | "passphrase";
