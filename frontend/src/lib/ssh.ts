// Pure helpers shared by the SSH page components.

import type { SshHost, SshHostInput, SshOption } from "./types";

/** ALIAS_RE mirrors the server-side alias rule exactly. */
export const ALIAS_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export function emptyHostInput(): SshHostInput {
  return {
    alias: "",
    hostName: "",
    user: "",
    port: "",
    identityFile: [],
    proxyJump: "",
    comment: "",
    options: [],
  };
}

export function hostToInput(host: SshHost): SshHostInput {
  return {
    alias: host.alias,
    hostName: host.hostName ?? "",
    user: host.user ?? "",
    port: host.port ?? "",
    identityFile: [...(host.identityFile ?? [])],
    proxyJump: host.proxyJump ?? "",
    comment: host.comment ?? "",
    options: (host.options ?? []).map((o) => ({ key: o.key, value: o.value })),
  };
}

/** normalizeInput trims every field and drops blank rows. */
export function normalizeInput(input: SshHostInput): SshHostInput {
  return {
    alias: input.alias.trim(),
    hostName: input.hostName.trim(),
    user: input.user.trim(),
    port: input.port.trim(),
    identityFile: input.identityFile.map((f) => f.trim()).filter(Boolean),
    proxyJump: input.proxyJump.trim(),
    comment: input.comment.trim(),
    options: input.options
      .map((o) => ({ key: o.key.trim(), value: o.value.trim() }))
      .filter((o) => o.key !== ""),
  };
}

/**
 * validateInput reproduces the server-side validation so the user gets feedback
 * before a round trip. The server remains the authority.
 */
export function validateInput(input: SshHostInput): string {
  if (!input.alias) return "Alias wajib diisi.";
  if (!ALIAS_RE.test(input.alias)) {
    return "Alias hanya boleh huruf, angka, titik, garis bawah dan strip, serta tidak diawali tanda baca.";
  }
  if (input.port) {
    const n = Number(input.port);
    if (!Number.isInteger(n) || n < 1 || n > 65535) {
      return "Port harus angka 1-65535.";
    }
  }
  const single = [
    ["HostName", input.hostName],
    ["User", input.user],
    ["ProxyJump", input.proxyJump],
    ["Deskripsi", input.comment],
  ] as const;
  for (const [label, value] of single) {
    if (/[\r\n\0]/.test(value)) return `${label} tidak boleh mengandung baris baru.`;
  }
  for (const file of input.identityFile) {
    if (/[\r\n\0]/.test(file)) return "IdentityFile tidak boleh mengandung baris baru.";
  }
  if (input.options.length > 64) return "Maksimal 64 opsi tambahan.";
  for (const opt of input.options) {
    if (!/^[A-Za-z][A-Za-z0-9]{0,63}$/.test(opt.key)) {
      return `Nama opsi "${opt.key}" tidak valid (diawali huruf, tanpa spasi).`;
    }
    if (/[\r\n\0]/.test(opt.value)) return `Nilai ${opt.key} tidak boleh mengandung baris baru.`;
  }
  return "";
}

/** quoteValue mirrors the writer's quoting for the live preview. */
function quoteValue(v: string): string {
  if (v === "") return `""`;
  if (!/[\s"'\\]/.test(v) && !v.startsWith("#")) return v;
  return `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

/** renderHostPreview shows exactly what will be appended to ~/.ssh/config. */
export function renderHostPreview(input: SshHostInput): string {
  const out: string[] = [];
  const comment = input.comment.replace(/[\r\n]+/g, " ").trim();
  if (comment) out.push(`# ${comment}`);
  out.push(`Host ${input.alias || "<alias>"}`);
  if (input.hostName) out.push(`    HostName ${quoteValue(input.hostName)}`);
  if (input.user) out.push(`    User ${quoteValue(input.user)}`);
  if (input.port) out.push(`    Port ${input.port}`);
  for (const file of input.identityFile) {
    if (file) out.push(`    IdentityFile ${quoteValue(file)}`);
  }
  if (input.proxyJump) out.push(`    ProxyJump ${quoteValue(input.proxyJump)}`);
  for (const opt of input.options) {
    if (!opt.key) continue;
    out.push(`    ${opt.key}${opt.value ? ` ${opt.value}` : ""}`);
  }
  return out.join("\n");
}

/** hostEndpoint renders "user@hostname:port" with sensible fallbacks. */
export function hostEndpoint(host: {
  alias: string;
  hostName?: string | null;
  user?: string | null;
  port?: string | null;
}): string {
  const hostPart = host.hostName || host.alias;
  const port = host.port && host.port !== "22" ? `:${host.port}` : "";
  return `${host.user ? `${host.user}@` : ""}${hostPart}${port}`;
}

/** optionValue returns the value of a directive from an ssh -G dump. */
export function optionValue(options: SshOption[] | null, key: string): string {
  if (!options) return "";
  const found = options.find((o) => o.key === key);
  return found ? found.value : "";
}

/** filterHosts matches a host against a free-text query. */
export function filterHosts(hosts: SshHost[], query: string): SshHost[] {
  const q = query.trim().toLowerCase();
  if (!q) return hosts;
  return hosts.filter((host) =>
    [host.alias, host.hostName, host.user, host.note, host.comment, host.proxyJump]
      .filter(Boolean)
      .some((field) => String(field).toLowerCase().includes(q)),
  );
}
