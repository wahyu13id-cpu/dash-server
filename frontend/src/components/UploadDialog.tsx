import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, uploadFile, type UploadTask } from "../lib/api";
import { formatBytes } from "../lib/format";
import { Button } from "./Button";
import { Modal } from "./Modal";
import { IconRefresh, IconUpload } from "./Icons";

type UploadStatus = "ready" | "uploading" | "done" | "conflict" | "error";

interface UploadItem {
  file: File;
  status: UploadStatus;
  loaded: number;
  error: string;
}

interface UploadDialogProps {
  open: boolean;
  /** Directory the files are stored into. */
  dir: string;
  /** Files chosen or dropped by the user. A new array starts a new batch. */
  files: File[];
  /** Names already present in the target directory. */
  existing: Set<string>;
  /** Per-file size limit advertised by the server. */
  maxBytes: number;
  onClose: () => void;
  /** Called once at least one file has been stored. */
  onUploaded: () => void;
}

function percentOf(item: UploadItem): number {
  if (item.status === "done") return 100;
  if (item.file.size <= 0) return 0;
  return Math.min(100, Math.round((item.loaded / item.file.size) * 100));
}

/**
 * UploadDialog stores the selected files one after another so every file gets
 * its own progress bar, its own error and, when the name already exists, its own
 * overwrite decision. Nothing leaves the machine until the user confirms the
 * list, and the whole batch is uploaded to the directory it was started in.
 */
export function UploadDialog({ open, dir, files, existing, maxBytes, onClose, onUploaded }: UploadDialogProps) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const [running, setRunning] = useState(false);
  const [aborted, setAborted] = useState(false);
  const taskRef = useRef<UploadTask | null>(null);
  const cancelRef = useRef(false);

  // A new batch (or a reopen) resets the list back to "ready".
  useEffect(() => {
    if (!open) return;
    cancelRef.current = false;
    taskRef.current = null;
    setRunning(false);
    setAborted(false);
    setItems(files.map((file) => ({ file, status: "ready", loaded: 0, error: "" })));
  }, [open, files]);

  const patch = useCallback((index: number, next: Partial<UploadItem>) => {
    setItems((prev) => prev.map((item, i) => (i === index ? { ...item, ...next } : item)));
  }, []);

  const runQueue = useCallback(
    async (indexes: number[], overwrite: boolean) => {
      cancelRef.current = false;
      setRunning(true);
      setAborted(false);
      let stored = 0;
      for (const i of indexes) {
        if (cancelRef.current) break;
        const file = files[i];
        if (!file) continue;
        if (file.size > maxBytes) {
          patch(i, { status: "error", loaded: 0, error: `Melebihi batas ${formatBytes(maxBytes)}.` });
          continue;
        }
        // A name that is already listed is decided before anything is sent: no
        // point pushing a large file only to be told the name is taken.
        if (!overwrite && existing.has(file.name)) {
          patch(i, { status: "conflict", error: "Nama sudah ada di folder ini." });
          continue;
        }
        patch(i, { status: "uploading", loaded: 0, error: "" });
        const task = uploadFile(dir, file, {
          overwrite,
          onProgress: (loaded) => patch(i, { loaded }),
        });
        taskRef.current = task;
        try {
          await task.promise;
          patch(i, { status: "done", loaded: file.size, error: "" });
          stored += 1;
        } catch (err) {
          if ((err as Error).name === "AbortError") {
            patch(i, { status: "ready", loaded: 0, error: "" });
          } else if (err instanceof ApiError && err.status === 409) {
            patch(i, { status: "conflict", error: "Nama sudah ada di folder ini." });
          } else {
            patch(i, { status: "error", error: err instanceof Error ? err.message : "Unggahan gagal." });
          }
        } finally {
          taskRef.current = null;
        }
      }
      cancelRef.current = false;
      setRunning(false);
      if (stored > 0) onUploaded();
    },
    [dir, existing, files, maxBytes, onUploaded, patch],
  );

  const cancel = () => {
    cancelRef.current = true;
    taskRef.current?.abort();
    setAborted(true);
  };

  // Closing is blocked while a transfer is in flight so an accidental Escape or
  // backdrop click cannot silently abandon the batch.
  const requestClose = () => {
    if (running) return;
    onClose();
  };

  const totalBytes = items.reduce((sum, item) => sum + item.file.size, 0);
  const doneBytes = items.reduce(
    (sum, item) => sum + (item.status === "done" ? item.file.size : item.loaded),
    0,
  );
  const doneCount = items.filter((item) => item.status === "done").length;
  const conflictCount = items.filter((item) => item.status === "conflict").length;
  const failedCount = items.filter((item) => item.status === "error").length;
  const pendingIndexes = items
    .map((item, index) => (item.status === "ready" || item.status === "error" ? index : -1))
    .filter((index) => index >= 0);
  const conflictIndexes = items
    .map((item, index) => (item.status === "conflict" ? index : -1))
    .filter((index) => index >= 0);
  const percent = totalBytes > 0 ? Math.round((doneBytes / totalBytes) * 100) : 0;

  const footer = running ? (
    <Button variant="secondary" onClick={cancel}>
      Batalkan
    </Button>
  ) : conflictCount > 0 ? (
    <Button variant="danger" onClick={() => void runQueue(conflictIndexes, true)}>
      Timpa {conflictCount} file
    </Button>
  ) : pendingIndexes.length > 0 ? (
    <Button
      variant="primary"
      icon={<IconUpload width={16} height={16} />}
      onClick={() => void runQueue(pendingIndexes, false)}
    >
      Unggah {pendingIndexes.length} file
    </Button>
  ) : (
    <Button variant="secondary" onClick={requestClose}>
      Selesai
    </Button>
  );

  return (
    <Modal
      open={open}
      title={`Unggah ke ${dir}`}
      onClose={requestClose}
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={requestClose} disabled={running}>
            Tutup
          </Button>
          {footer}
        </>
      }
    >
      <div className="upload">
        <div className="upload__head">
          <span>
            {doneCount > 0 ? `${doneCount} dari ${items.length} file` : `${items.length} file`} ·{" "}
            {formatBytes(doneBytes)} / {formatBytes(totalBytes)}
          </span>
          <span className="upload__percent">{percent}%</span>
        </div>
        <div className="upload__bar" aria-hidden="true">
          <span className="upload__bar-fill" style={{ width: `${percent}%` }} />
        </div>

        <ul className="upload__list">
          {items.map((item, index) => (
            <li className={`upload__row upload__row--${item.status}`} key={`${item.file.name}-${index}`}>
              <div className="upload__row-main">
                <span className="upload__name" title={item.file.name}>
                  {item.file.name}
                </span>
                <span className="upload__size">{formatBytes(item.file.size)}</span>
                <span className={`upload__state upload__state--${item.status}`}>
                  {item.status === "ready" && (aborted ? "Dibatalkan" : "Menunggu")}
                  {item.status === "uploading" && `${percentOf(item)}%`}
                  {item.status === "done" && "Selesai"}
                  {item.status === "conflict" && "Sudah ada"}
                  {item.status === "error" && "Gagal"}
                </span>
                {!running && item.status === "error" && (
                  <button
                    className="link-btn"
                    type="button"
                    onClick={() => void runQueue([index], false)}
                    title="Ulangi file ini"
                  >
                    <IconRefresh width={14} height={14} />
                    <span>Ulangi</span>
                  </button>
                )}
              </div>
              {item.status === "uploading" && (
                <div className="upload__row-bar">
                  <span style={{ width: `${percentOf(item)}%` }} />
                </div>
              )}
              {item.error && <p className="upload__row-error">{item.error}</p>}
            </li>
          ))}
        </ul>

        <p className="upload__hint">
          {conflictCount > 0 ? (
            <>
              {conflictCount} file sudah ada di <code>{dir}</code>. Pilih “Timpa” untuk menggantinya, atau tutup
              dialog untuk melewatinya.
            </>
          ) : failedCount > 0 ? (
            `${failedCount} file gagal diunggah.`
          ) : (
            <>
              File disimpan langsung ke <code>{dir}</code> dengan nama yang sama.
            </>
          )}
        </p>
      </div>
    </Modal>
  );
}
