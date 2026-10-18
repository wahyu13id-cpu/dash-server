import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "../../lib/api";
import { Button } from "../Button";
import { Modal } from "../Modal";
import { hostEndpoint } from "../../lib/ssh";
import type { SshHostInput, SshQuickAdd } from "../../lib/types";

interface SshQuickAddProps {
  open: boolean;
  busy: boolean;
  onClose: () => void;
  onCreate: (input: SshHostInput) => void;
}

const EXAMPLE = "ssh deploy@web1.example.com -p 2222 -i ~/.ssh/id_ed25519";

/**
 * SshQuickAdd lets the user paste the ssh command they would normally type and
 * turns it into a host entry. Parsing happens on the server so the result is
 * identical to what the command line would mean.
 */
export function SshQuickAdd({ open, busy, onClose, onCreate }: SshQuickAddProps) {
  const [command, setCommand] = useState("");
  const [parsed, setParsed] = useState<SshQuickAdd | null>(null);
  const [parsing, setParsing] = useState(false);
  const [error, setError] = useState("");
  const timer = useRef<number | null>(null);

  useEffect(() => {
    if (open) {
      setCommand("");
      setParsed(null);
      setError("");
    }
  }, [open]);

  const analyze = useCallback(async (text: string) => {
    if (!text.trim()) {
      setParsed(null);
      return;
    }
    setParsing(true);
    try {
      const result = await api.sshParse(text);
      setParsed(result);
      setError("");
    } catch (err) {
      setParsed(null);
      setError(err instanceof Error ? err.message : "Gagal menganalisis perintah.");
    } finally {
      setParsing(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      void analyze(command);
    }, 350);
    return () => {
      if (timer.current) window.clearTimeout(timer.current);
    };
  }, [command, open, analyze]);

  const submit = () => {
    if (!parsed || !parsed.alias) {
      setError("Perintah belum menghasilkan host yang bisa dipakai.");
      return;
    }
    onCreate({
      alias: parsed.alias,
      hostName: parsed.hostName,
      user: parsed.user,
      port: parsed.port,
      identityFile: parsed.identityFile ? [parsed.identityFile] : [],
      proxyJump: parsed.proxyJump,
      comment: "",
      options: [],
    });
  };

  return (
    <Modal
      open={open}
      title="Impor dari perintah ssh"
      size="md"
      onClose={busy ? () => undefined : onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!parsed?.alias}>
            Tambah host
          </Button>
        </>
      }
    >
      <div className="form-stack">
        <label className="field">
          <span>Tempel perintah</span>
          <textarea
            className="ssh-quick__input"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder={EXAMPLE}
            rows={3}
            spellCheck={false}
            autoFocus
          />
          <small className="field__hint">
            Contoh: <code>{EXAMPLE}</code>
          </small>
        </label>

        {parsing && <p className="muted">Menganalisis...</p>}

        {parsed && parsed.alias && (
          <div className="ssh-quick__result">
            <div className="ssh-quick__result-head">
              <span>Hasil</span>
              <code>{hostEndpoint(parsed)}</code>
            </div>
            <dl className="ssh-quick__grid">
              <div>
                <dt>Alias</dt>
                <dd>
                  <code>{parsed.alias}</code>
                </dd>
              </div>
              <div>
                <dt>HostName</dt>
                <dd>
                  <code>{parsed.hostName || parsed.alias}</code>
                </dd>
              </div>
              <div>
                <dt>User</dt>
                <dd>{parsed.user || <span className="muted">-</span>}</dd>
              </div>
              <div>
                <dt>Port</dt>
                <dd>{parsed.port || <span className="muted">22</span>}</dd>
              </div>
              {parsed.identityFile && (
                <div>
                  <dt>IdentityFile</dt>
                  <dd>
                    <code>{parsed.identityFile}</code>
                  </dd>
                </div>
              )}
              {parsed.proxyJump && (
                <div>
                  <dt>ProxyJump</dt>
                  <dd>
                    <code>{parsed.proxyJump}</code>
                  </dd>
                </div>
              )}
            </dl>
            {(parsed.warnings ?? []).length > 0 && (
              <ul className="ssh-quick__warnings">
                {(parsed.warnings ?? []).map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            )}
          </div>
        )}

        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}
