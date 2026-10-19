import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../lib/api";
import type { LogEntry, LogSource, LogSourceList } from "../lib/types";
import { useOps } from "../context/OpsContext";
import { useToast } from "../context/ToastContext";
import { Button, IconButton } from "../components/Button";
import { Card } from "../components/Card";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { IconCopy, IconDownload, IconRefresh, IconScroll, IconSearch } from "../components/Icons";
import { JOURNAL_PRIORITIES, JOURNAL_WINDOWS, priorityLabel } from "../lib/ops";

interface Selection {
  kind: "journal" | "unit" | "file";
  name: string;
  unit?: string;
  path?: string;
}

export function LogsPage() {
  const toast = useToast();
  const { availability } = useOps();

  const [sources, setSources] = useState<LogSourceList | null>(null);
  const [sourceQuery, setSourceQuery] = useState("");
  const [selection, setSelection] = useState<Selection | null>(null);
  const [loadingSources, setLoadingSources] = useState(true);

  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [fileLines, setFileLines] = useState<string[]>([]);
  const [fileMeta, setFileMeta] = useState<{ size: number; truncated: boolean } | null>(null);
  const [loadingLog, setLoadingLog] = useState(false);
  const [logError, setLogError] = useState("");

  const [priority, setPriority] = useState("");
  const [since, setSince] = useState("");
  const [search, setSearch] = useState("");
  // The journal search runs a real journalctl call, so it is debounced rather
  // than fired on every keystroke.
  const [activeSearch, setActiveSearch] = useState("");
  const [lines, setLines] = useState(300);
  const [auto, setAuto] = useState(true);
  const [wrap, setWrap] = useState(false);
  const [nonce, setNonce] = useState(0);

  const viewRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);

  useEffect(() => {
    const timer = window.setTimeout(() => setActiveSearch(search.trim()), 350);
    return () => window.clearTimeout(timer);
  }, [search]);

  // Load the source catalogue once, then pick the system journal.
  useEffect(() => {
    let active = true;
    api
      .opsLogSources()
      .then((list) => {
        if (!active) return;
        setSources(list);
        const first = (list.sources ?? []).find((s) => s.kind === "journal") ?? list.sources?.[0];
        if (first) setSelection({ kind: first.kind, name: first.name, unit: first.unit, path: first.path });
      })
      .catch((err) => {
        if (active) toast.error(err instanceof Error ? err.message : "Gagal memuat daftar log.");
      })
      .finally(() => {
        if (active) setLoadingSources(false);
      });
    return () => {
      active = false;
    };
  }, [toast]);

  const loadLog = useCallback(
    async (background = false) => {
      if (!selection) return;
      if (!background) setLoadingLog(true);
      try {
        if (selection.kind === "file") {
          const file = await api.opsLogFile({
            path: selection.path ?? "",
            lines,
            search: activeSearch || undefined,
          });
          setFileLines(file.lines ?? []);
          setFileMeta({ size: file.size, truncated: file.truncated });
          setEntries([]);
        } else {
          const res = await api.opsJournal({
            unit: selection.kind === "unit" ? selection.unit : undefined,
            priority: priority || undefined,
            since: since || undefined,
            lines,
            search: activeSearch || undefined,
          });
          setEntries(res.entries ?? []);
          setFileLines([]);
          setFileMeta(null);
        }
        setLogError("");
      } catch (err) {
        if (!background) setLogError(err instanceof Error ? err.message : "Gagal memuat log.");
      } finally {
        if (!background) setLoadingLog(false);
      }
    },
    [selection, lines, priority, since, activeSearch],
  );

  useEffect(() => {
    void loadLog();
  }, [loadLog, nonce]);

  useEffect(() => {
    if (!auto) return;
    const timer = window.setInterval(() => setNonce((n) => n + 1), 3000);
    return () => window.clearInterval(timer);
  }, [auto, selection]);

  // Keep the newest line in view while following the log.
  useEffect(() => {
    const node = viewRef.current;
    if (!node || !followRef.current) return;
    node.scrollTop = node.scrollHeight;
  }, [entries, fileLines]);

  const grouped = useMemo(() => {
    const all = sources?.sources ?? [];
    const needle = sourceQuery.trim().toLowerCase();
    const filtered = needle ? all.filter((s) => s.name.toLowerCase().includes(needle)) : all;
    return {
      journal: filtered.filter((s) => s.kind !== "file"),
      files: filtered.filter((s) => s.kind === "file"),
    };
  }, [sources, sourceQuery]);

  const plainText = useMemo(() => {
    if (selection?.kind === "file") return fileLines.join("\n");
    return entries
      .map((e) => `${e.time} [${priorityLabel(e.priority)}] ${e.unit || e.identifier || "-"}: ${e.message}`)
      .join("\n");
  }, [entries, fileLines, selection]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(plainText);
      toast.success("Log disalin ke papan klip.");
    } catch {
      toast.error("Gagal menyalin log.");
    }
  };

  const download = () => {
    const blob = new Blob([plainText + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${(selection?.name ?? "log").replace(/[^\w.-]+/g, "_")}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  if (availability && !availability.journal && !availability.logDir) {
    return (
      <div className="page">
        <Card title="Log sistem">
          <EmptyState
            icon={<IconScroll width={22} height={22} />}
            title="Tidak ada sumber log yang bisa dibaca"
            description="Host ini tidak menyediakan journald maupun direktori /var/log."
          />
        </Card>
      </div>
    );
  }

  if (loadingSources) {
    return (
      <div className="page">
        <Spinner label="Memuat sumber log..." />
      </div>
    );
  }

  return (
    <div className="page page--logs">
      <div className="logs-layout">
        <Card
          title="Sumber log"
          subtitle={`${(sources?.sources ?? []).length} sumber`}
          padded={false}
          className="logs-sources"
        >
          <div className="ops-toolbar ops-toolbar--padded">
            <span className="ops-search">
              <IconSearch width={15} height={15} />
              <input
                value={sourceQuery}
                onChange={(e) => setSourceQuery(e.target.value)}
                placeholder="Cari sumber..."
                aria-label="Cari sumber log"
                spellCheck={false}
              />
            </span>
          </div>
          <div className="logs-sources__list">
            <SourceGroup
              label="Journal"
              sources={grouped.journal}
              selection={selection}
              onSelect={setSelection}
            />
            <SourceGroup
              label={`Berkas (${sources?.logDir ?? ""})`}
              sources={grouped.files}
              selection={selection}
              onSelect={setSelection}
            />
          </div>
        </Card>

        <Card
          padded={false}
          className="logs-viewer"
          title={selection ? selection.name : "Log"}
          subtitle={
            selection?.kind === "file" && fileMeta
              ? `${fileMeta.size.toLocaleString()} byte${fileMeta.truncated ? " · menampilkan ekor berkas" : ""}`
              : loadingLog
                ? "memuat..."
                : `${entries.length} entri terakhir`
          }
          actions={
            <div className="logs-viewer__actions">
              <label className="ops-toggle">
                <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
                <span>Otomatis</span>
              </label>
              <label className="ops-toggle">
                <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
                <span>Bungkus baris</span>
              </label>
              <IconButton label="Salin" onClick={() => void copy()}>
                <IconCopy width={15} height={15} />
              </IconButton>
              <IconButton label="Unduh" onClick={download}>
                <IconDownload width={15} height={15} />
              </IconButton>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setNonce((n) => n + 1)}
                icon={<IconRefresh width={15} height={15} />}
              >
                Muat ulang
              </Button>
            </div>
          }
        >
          <div className="ops-toolbar ops-toolbar--padded logs-filters">
            {selection?.kind !== "file" && (
              <>
                <label className="ops-field">
                  <span>Level</span>
                  <select value={priority} onChange={(e) => setPriority(e.target.value)}>
                    {JOURNAL_PRIORITIES.map((p) => (
                      <option key={p.value} value={p.value}>
                        {p.label}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="ops-field">
                  <span>Rentang</span>
                  <select value={since} onChange={(e) => setSince(e.target.value)}>
                    {JOURNAL_WINDOWS.map((w) => (
                      <option key={w.value} value={w.value}>
                        {w.label}
                      </option>
                    ))}
                  </select>
                </label>
              </>
            )}
            <label className="ops-field">
              <span>Baris</span>
              <select value={lines} onChange={(e) => setLines(Number(e.target.value))}>
                {[100, 300, 500, 1000, 2000].map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <label className="ops-field ops-field--grow">
              <span>Cari</span>
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={
                  selection?.kind === "file"
                    ? "Saring baris yang mengandung..."
                    : "Cari di seluruh journal..."
                }
                spellCheck={false}
              />
            </label>
          </div>

          {logError ? (
            <div className="ops-pad">
              <ErrorState message={logError} onRetry={() => setNonce((n) => n + 1)} />
            </div>
          ) : loadingLog && entries.length === 0 && fileLines.length === 0 ? (
            <Spinner label="Memuat log..." />
          ) : plainText === "" ? (
            <div className="ops-pad">
              <EmptyState
                icon={<IconScroll width={22} height={22} />}
                title="Tidak ada entri"
                description="Coba longgarkan filter level, rentang waktu, atau kata kunci."
              />
            </div>
          ) : (
            <div
              className={`log-view ${wrap ? "log-view--wrap" : ""}`}
              ref={viewRef}
              onScroll={(e) => {
                const el = e.currentTarget;
                followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
              }}
            >
              {selection?.kind === "file"
                ? fileLines.map((line, index) => (
                    <div className="log-line log-line--plain" key={index}>
                      <span className="log-line__msg">{line}</span>
                    </div>
                  ))
                : entries.map((entry, index) => (
                    <div className="log-line" key={`${entry.seq}-${index}`}>
                      <span className="log-line__time">
                        {new Date(entry.time).toLocaleTimeString(undefined, { hour12: false })}
                      </span>
                      <span className={`log-line__prio log-line__prio--${entry.priority}`}>
                        {priorityLabel(entry.priority)}
                      </span>
                      <span className="log-line__unit">{entry.unit || entry.identifier || "-"}</span>
                      <span className="log-line__msg">{entry.message}</span>
                    </div>
                  ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}

function SourceGroup({
  label,
  sources,
  selection,
  onSelect,
}: {
  label: string;
  sources: LogSource[];
  selection: Selection | null;
  onSelect: (s: Selection) => void;
}) {
  if (sources.length === 0) return null;
  return (
    <div className="logs-group">
      <div className="logs-group__label">{label}</div>
      {sources.map((source) => {
        const active =
          selection?.kind === source.kind &&
          selection?.name === source.name &&
          selection?.path === source.path;
        return (
          <button
            key={`${source.kind}:${source.name}`}
            type="button"
            className={`logs-source ${active ? "logs-source--active" : ""}`}
            onClick={() =>
              onSelect({ kind: source.kind, name: source.name, unit: source.unit, path: source.path })
            }
          >
            <span className="logs-source__name">{source.name}</span>
            {source.kind === "file" && source.size !== undefined && (
              <span className="logs-source__meta">{(source.size / 1024).toFixed(0)} KB</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
