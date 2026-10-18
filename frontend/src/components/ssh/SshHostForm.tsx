import { useEffect, useMemo, useState } from "react";

import { Button } from "../Button";
import { Modal } from "../Modal";
import { IconPlus, IconTrash } from "../Icons";
import { normalizeInput, renderHostPreview, validateInput } from "../../lib/ssh";import type { SshHostInput, SshKey, SshOption } from "../../lib/types";

interface SshHostFormProps {
  open: boolean;
  mode: "create" | "edit";
  initial: SshHostInput;
  keys: SshKey[];
  busy: boolean;
  serverError: string;
  onSubmit: (input: SshHostInput) => void;
  onClose: () => void;
}

const COMMON_OPTIONS = [
  "ForwardAgent",
  "ForwardX11",
  "ServerAliveInterval",
  "ServerAliveCountMax",
  "TCPKeepAlive",
  "Compression",
  "ProxyCommand",
  "StrictHostKeyChecking",
  "UserKnownHostsFile",
  "RequestTTY",
  "RemoteCommand",
  "LocalForward",
  "RemoteForward",
  "DynamicForward",
  "AddKeysToAgent",
  "IdentitiesOnly",
  "PubkeyAuthentication",
  "PreferredAuthentications",
  "SetEnv",
];

export function SshHostForm({
  open,
  mode,
  initial,
  keys,
  busy,
  serverError,
  onSubmit,
  onClose,
}: SshHostFormProps) {
  const [draft, setDraft] = useState<SshHostInput>(initial);
  const [localError, setLocalError] = useState("");
  const [showPreview, setShowPreview] = useState(true);

  useEffect(() => {
    if (open) {
      setDraft(initial);
      setLocalError("");
      setShowPreview(true);
    }
  }, [open, initial]);

  const normalized = useMemo(() => normalizeInput(draft), [draft]);
  const preview = useMemo(() => renderHostPreview(normalized), [normalized]);

  const set = <K extends keyof SshHostInput>(key: K, value: SshHostInput[K]) =>
    setDraft((prev) => ({ ...prev, [key]: value }));

  const setOption = (index: number, patch: Partial<SshOption>) =>
    setDraft((prev) => ({
      ...prev,
      options: prev.options.map((opt, i) => (i === index ? { ...opt, ...patch } : opt)),
    }));

  const submit = () => {
    const invalid = validateInput(normalized);
    if (invalid) {
      setLocalError(invalid);
      return;
    }
    setLocalError("");
    onSubmit(normalized);
  };

  const error = localError || serverError;

  return (
    <Modal
      open={open}
      title={mode === "create" ? "Host SSH baru" : `Ubah host ${initial.alias}`}
      size="lg"
      onClose={busy ? () => undefined : onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={submit} loading={busy}>
            {mode === "create" ? "Tambah host" : "Simpan perubahan"}
          </Button>
        </>
      }
    >
      <div className="ssh-form">
        <div className="ssh-form__fields">
          <div className="ssh-form__row ssh-form__row--2">
            <label className="field">
              <span>
                Alias <em className="req">*</em>
              </span>
              <input
                value={draft.alias}
                onChange={(e) => set("alias", e.target.value)}
                placeholder="web-produksi"
                spellCheck={false}
                autoComplete="off"
                autoFocus
              />
              <small className="field__hint">
                Dipakai saat menyambung: <code>ssh {draft.alias || "<alias>"}</code>
              </small>
            </label>
            <label className="field">
              <span>HostName</span>
              <input
                value={draft.hostName}
                onChange={(e) => set("hostName", e.target.value)}
                placeholder="10.0.0.12 atau host.example.com"
                spellCheck={false}
                autoComplete="off"
              />
              <small className="field__hint">Kosong berarti sama dengan alias.</small>
            </label>
          </div>

          <div className="ssh-form__row ssh-form__row--3">
            <label className="field">
              <span>User</span>
              <input
                value={draft.user}
                onChange={(e) => set("user", e.target.value)}
                placeholder="root"
                spellCheck={false}
                autoComplete="off"
              />
            </label>
            <label className="field">
              <span>Port</span>
              <input
                value={draft.port}
                onChange={(e) => set("port", e.target.value)}
                placeholder="22"
                inputMode="numeric"
                spellCheck={false}
              />
            </label>
            <label className="field">
              <span>ProxyJump</span>
              <input
                value={draft.proxyJump}
                onChange={(e) => set("proxyJump", e.target.value)}
                placeholder="bastion"
                spellCheck={false}
                autoComplete="off"
              />
            </label>
          </div>

          <div className="field">
            <span>IdentityFile</span>
            <div className="ssh-form__list">
              {draft.identityFile.length === 0 && (
                <p className="ssh-form__empty">
                  Tanpa IdentityFile, ssh memakai kunci default atau agen kunci bila ada.
                </p>
              )}
              {draft.identityFile.map((file, index) => (
                <div className="ssh-form__list-row" key={`id-${index}`}>
                  <input
                    value={file}
                    list="ssh-key-options"
                    onChange={(e) =>
                      set(
                        "identityFile",
                        draft.identityFile.map((f, i) => (i === index ? e.target.value : f)),
                      )
                    }
                    placeholder="~/.ssh/id_ed25519"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <button
                    type="button"
                    className="icon-btn icon-btn--ghost"
                    aria-label="Hapus kunci"
                    onClick={() =>
                      set(
                        "identityFile",
                        draft.identityFile.filter((_, i) => i !== index),
                      )
                    }
                  >
                    <IconTrash width={15} height={15} />
                  </button>
                </div>
              ))}
              <datalist id="ssh-key-options">
                {keys.map((key) => (
                  <option key={key.name} value={key.path}>
                    {key.type ? `${key.type} ${key.fingerprint ?? ""}` : key.name}
                  </option>
                ))}
              </datalist>
              <Button
                variant="ghost"
                size="sm"
                icon={<IconPlus width={14} height={14} />}
                onClick={() => set("identityFile", [...draft.identityFile, ""])}
              >
                Tambah kunci
              </Button>
            </div>
          </div>

          <label className="field">
            <span>Deskripsi</span>
            <input
              value={draft.comment}
              onChange={(e) => set("comment", e.target.value)}
              placeholder="Server produksi aplikasi utama"
            />
            <small className="field__hint">Ditulis sebagai komentar di atas blok Host.</small>
          </label>

          <div className="field">
            <span>Opsi lanjutan</span>
            <div className="ssh-form__list">
              {draft.options.length === 0 && (
                <p className="ssh-form__empty">
                  Terjemahan langsung menjadi direktif ssh_config, mis.{" "}
                  <code>ForwardAgent yes</code>.
                </p>
              )}
              {draft.options.map((opt, index) => (
                <div className="ssh-form__list-row ssh-form__list-row--pair" key={`opt-${index}`}>
                  <input
                    value={opt.key}
                    list="ssh-option-keys"
                    onChange={(e) => setOption(index, { key: e.target.value })}
                    placeholder="ForwardAgent"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <input
                    value={opt.value}
                    onChange={(e) => setOption(index, { value: e.target.value })}
                    placeholder="yes"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <button
                    type="button"
                    className="icon-btn icon-btn--ghost"
                    aria-label="Hapus opsi"
                    onClick={() =>
                      set(
                        "options",
                        draft.options.filter((_, i) => i !== index),
                      )
                    }
                  >
                    <IconTrash width={15} height={15} />
                  </button>
                </div>
              ))}
              <datalist id="ssh-option-keys">
                {COMMON_OPTIONS.map((key) => (
                  <option key={key} value={key} />
                ))}
              </datalist>
              <Button
                variant="ghost"
                size="sm"
                icon={<IconPlus width={14} height={14} />}
                onClick={() => set("options", [...draft.options, { key: "", value: "" }])}
              >
                Tambah opsi
              </Button>
            </div>
          </div>

          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
        </div>

        <div className="ssh-form__preview">
          <div className="ssh-form__preview-head">
            <span>Pratinjau blok konfigurasi</span>
            <button
              type="button"
              className="link-btn"
              onClick={() => setShowPreview((v) => !v)}
            >
              {showPreview ? "Sembunyikan" : "Tampilkan"}
            </button>
          </div>
          {showPreview && (
            <>
              <pre className="ssh-form__code">{preview}</pre>
              <p className="ssh-form__note">
                File <code>~/.ssh/config</code> tetap format OpenSSH standar, jadi bisa dipakai
                langsung oleh <code>ssh</code>, VSCode Remote-SSH, dan perkakas lain.
              </p>
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}
