import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";

import { api } from "../lib/api";
import { Button } from "../components/Button";
import { Badge } from "../components/Card";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { IconAlert, IconPlug, IconPlus, IconRefresh, IconSearch } from "../components/Icons";
import { useToast } from "../context/ToastContext";
import { SshDetail } from "../components/ssh/SshDetail";
import { SshHostForm } from "../components/ssh/SshHostForm";
import { SshHostList } from "../components/ssh/SshHostList";
import { SshQuickAdd } from "../components/ssh/SshQuickAdd";
import { SshDefaultsPanel, SshKeysPanel } from "../components/ssh/SshPanels";
import { emptyHostInput, filterHosts, hostToInput } from "../lib/ssh";
import type { SshHost, SshHostInput, SshState } from "../lib/types";

export function SshPage() {
  const toast = useToast();
  const navigate = useNavigate();

  const [state, setState] = useState<SshState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState("");

  const [formOpen, setFormOpen] = useState(false);
  const [formMode, setFormMode] = useState<"create" | "edit">("create");
  const [formInitial, setFormInitial] = useState<SshHostInput>(emptyHostInput);
  const [formBusy, setFormBusy] = useState(false);
  const [formError, setFormError] = useState("");

  const [quickOpen, setQuickOpen] = useState(false);
  const [quickBusy, setQuickBusy] = useState(false);

  const load = useCallback(
    async (keepSelection = true) => {
      try {
        const next = await api.sshState();
        setState(next);
        setError("");
        setSelected((current) => {
          const hosts = next.hosts ?? [];
          if (keepSelection && current && hosts.some((host) => host.alias === current)) {
            return current;
          }
          const favorite = hosts.find((host) => host.favorite);
          return hosts.length > 0 ? (favorite ?? hosts[0]).alias : "";
        });
      } catch (err) {
        setError(err instanceof Error ? err.message : "Tidak dapat memuat konfigurasi SSH.");
      } finally {
        setLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const hosts = useMemo(() => state?.hosts ?? [], [state]);
  const visibleHosts = useMemo(() => filterHosts(hosts, query), [hosts, query]);
  const activeHost = useMemo(
    () => hosts.find((host) => host.alias === selected) ?? null,
    [hosts, selected],
  );

  const secretIssue = hosts.some((host) => host.secretBroken);
  const readOnlyCount = hosts.filter((host) => !host.writable).length;

  const openCreate = () => {
    setFormMode("create");
    setFormInitial(emptyHostInput());
    setFormError("");
    setFormOpen(true);
  };

  const openEdit = (host: SshHost) => {
    setFormMode("edit");
    setFormInitial(hostToInput(host));
    setFormError("");
    setFormOpen(true);
  };

  const submitForm = async (input: SshHostInput) => {
    setFormBusy(true);
    setFormError("");
    try {
      if (formMode === "create") {
        const created = await api.sshCreateHost(input);
        toast.success(`Host ${created.alias} ditambahkan.`);
        setFormOpen(false);
        await load(false);
        setSelected(created.alias);
      } else {
        const updated = await api.sshUpdateHost(formInitial.alias, input);
        toast.success(
          updated.alias === formInitial.alias
            ? `Host ${updated.alias} diperbarui.`
            : `Host ${formInitial.alias} diganti nama menjadi ${updated.alias}.`,
        );
        setFormOpen(false);
        await load(false);
        setSelected(updated.alias);
      }
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "Gagal menyimpan host.");
    } finally {
      setFormBusy(false);
    }
  };

  const deleteHost = async (host: SshHost) => {
    try {
      await api.sshDeleteHost(host.alias);
      toast.success(`Host ${host.alias} dihapus.`);
      setSelected("");
      await load(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menghapus host.");
    }
  };

  const quickCreate = async (input: SshHostInput) => {
    setQuickBusy(true);
    try {
      const created = await api.sshCreateHost(input);
      toast.success(`Host ${created.alias} ditambahkan.`);
      setQuickOpen(false);
      await load(false);
      setSelected(created.alias);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menambahkan host.");
    } finally {
      setQuickBusy(false);
    }
  };

  const openTerminal = (alias: string) => {
    navigate(`/terminal?host=${encodeURIComponent(alias)}`);
  };

  if (loading) {
    return (
      <div className="page page--ssh">
        <Spinner label="Memuat konfigurasi SSH..." />
      </div>
    );
  }

  if (error && !state) {
    return (
      <div className="page page--ssh">
        <ErrorState message={error} onRetry={() => void load()} />
      </div>
    );
  }

  const disabled = state ? !state.enabled : false;
  const configMissing = state ? !state.exists : false;

  return (
    <div className="page page--ssh">
      <div className="ssh-toolbar">
        <label className="ssh-search">
          <IconSearch width={16} height={16} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Cari host, user, atau hostname..."
            spellCheck={false}
            aria-label="Cari host"
          />
        </label>
        <div className="ssh-toolbar__actions">
          <Button variant="ghost" size="sm" icon={<IconRefresh width={15} height={15} />} onClick={() => void load()}>
            Muat ulang
          </Button>
          <Button variant="secondary" size="sm" icon={<IconPlug width={15} height={15} />} onClick={() => setQuickOpen(true)}>
            Impor perintah ssh
          </Button>
          <Button variant="primary" size="sm" icon={<IconPlus width={15} height={15} />} onClick={openCreate}>
            Host baru
          </Button>
        </div>
      </div>

      {disabled && (
        <div className="ssh-banner ssh-banner--warning">
          <IconAlert width={16} height={16} />
          <div>
            <strong>Fitur SSH dimatikan.</strong> Aktifkan dengan{" "}
            <code>DASH_SSH_ENABLED=true</code> pada berkas environment lalu mulai ulang layanan.
          </div>
        </div>
      )}

      {configMissing && !disabled && (
        <div className="ssh-banner ssh-banner--info">
          <IconAlert width={16} height={16} />
          <div>
            <code>{state?.configPath}</code> belum ada. Berkas akan dibuat otomatis saat host
            pertama disimpan.
          </div>
        </div>
      )}

      {secretIssue && (
        <div className="ssh-banner ssh-banner--warning">
          <IconAlert width={16} height={16} />
          <div>
            <strong>Sebagian kredensial tidak dapat dibuka.</strong> Kunci enkripsi
            (<code>DASH_SESSION_SECRET</code>) berubah. Simpan ulang kredensial pada host terkait.
          </div>
        </div>
      )}

      {(state?.warnings ?? []).length > 0 && (
        <div className="ssh-banner ssh-banner--info">
          <IconAlert width={16} height={16} />
          <div>
            <strong>Peringatan parser:</strong>
            <ul className="ssh-banner__list">
              {(state?.warnings ?? []).map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </div>
        </div>
      )}

      <div className="ssh-layout">
        <aside className="ssh-sidebar">
          <div className="ssh-sidebar__head">
            <h2>Host ({hosts.length})</h2>
            {readOnlyCount > 0 && <Badge tone="neutral">{readOnlyCount} hanya baca</Badge>}
          </div>
          <div className="ssh-sidebar__scroll">
            {hosts.length === 0 ? (
              <EmptyState
                icon={<IconPlug width={24} height={24} />}
                title="Belum ada host SSH"
                description="Tambahkan host baru, atau tempel perintah ssh yang biasa Anda pakai."
                action={
                  <div className="ssh-empty-actions">
                    <Button variant="primary" size="sm" onClick={openCreate}>
                      Tambah host
                    </Button>
                    <Button variant="secondary" size="sm" onClick={() => setQuickOpen(true)}>
                      Impor perintah
                    </Button>
                  </div>
                }
              />
            ) : (
              <SshHostList
                hosts={visibleHosts}
                selected={selected}
                onSelect={setSelected}
                onToggleFavorite={() => void load()}
              />
            )}
            <SshKeysPanel keys={state?.keys ?? []} />
            <SshDefaultsPanel defaults={state?.defaults ?? []} />
            <div className="ssh-side-panel ssh-files">
              <h3 className="ssh-side-panel__title">Berkas konfigurasi</h3>
              <ul>
                {(state?.files ?? []).map((file) => (
                  <li key={file.path}>
                    <code>{file.path}</code>
                    <span className="ssh-files__badges">
                      {file.primary && <Badge tone="info">utama</Badge>}
                      <Badge tone={file.writable ? "success" : "neutral"}>
                        {file.writable ? "bisa tulis" : "hanya baca"}
                      </Badge>
                      {file.mode && <Badge tone="neutral">{file.mode}</Badge>}
                    </span>
                    {file.error && <span className="ssh-files__error">{file.error}</span>}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </aside>

        <section className="ssh-main">
          {activeHost ? (
            <SshDetail
              host={activeHost}
              keys={state?.keys ?? []}
              onEdit={openEdit}
              onDelete={deleteHost}
              onOpenTerminal={openTerminal}
              onToggleFavorite={() => void load()}
              onChanged={() => void load()}
            />
          ) : (
            <div className="ssh-placeholder">
              <EmptyState
                icon={<IconPlug width={26} height={26} />}
                title={hosts.length === 0 ? "Mulai dengan satu host" : "Pilih host"}
                description={
                  hosts.length === 0
                    ? "Setiap host yang Anda tambahkan ditulis sebagai blok Host di ~/.ssh/config, jadi bisa dipakai juga oleh ssh dan VSCode Remote-SSH."
                    : "Pilih host di kiri untuk melihat detail, kunci host, kredensial, dan konfigurasi efektifnya."
                }
                action={
                  hosts.length === 0 ? (
                    <Button variant="primary" size="sm" onClick={openCreate}>
                      Tambah host pertama
                    </Button>
                  ) : undefined
                }
              />
            </div>
          )}
        </section>
      </div>

      <SshHostForm
        open={formOpen}
        mode={formMode}
        initial={formInitial}
        keys={state?.keys ?? []}
        busy={formBusy}
        serverError={formError}
        onSubmit={submitForm}
        onClose={() => setFormOpen(false)}
      />

      <SshQuickAdd
        open={quickOpen}
        busy={quickBusy}
        onClose={() => setQuickOpen(false)}
        onCreate={quickCreate}
      />
    </div>
  );
}
