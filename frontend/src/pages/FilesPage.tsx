import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router-dom";

import { ApiError, api, archiveDownloadURL, fileDownloadURL } from "../lib/api";
import {
  MAX_FLOAT_WINDOWS,
  floatingTerminals,
} from "../lib/floatingTerminals";
import type { FileContent, FileEntry, FileListing, FileStat } from "../lib/types";
import {
  baseName,
  formatBytes,
  formatDateTime,
  joinPath,
  normalizePath,
  parentPath,
} from "../lib/format";
import { useToast } from "../context/ToastContext";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { Button, IconButton } from "../components/Button";
import { ContextMenu, useMenu, type MenuItem } from "../components/ContextMenu";
import { EmptyState, ErrorState, Spinner } from "../components/Feedback";
import { ConfirmDialog, Modal } from "../components/Modal";
import { UploadDialog } from "../components/UploadDialog";
import {
  IconArchive,
  IconAudio,
  IconChevronLeft,
  IconCheck,
  IconCode,
  IconCopy,
  IconDownload,
  IconEdit,
  IconFile,
  IconFolder,
  IconFolderUp,
  IconImage,
  IconInfo,
  IconLock,
  IconMore,
  IconPlus,
  IconRefresh,
  IconSortAsc,
  IconSortDesc,
  IconTerminal,
  IconText,
  IconTrash,
  IconUpload,
  IconVideo,
  IconViewCompact,
  IconViewGrid,
  IconViewList,
} from "../components/Icons";

// Marker type for dragging rows inside the page. It keeps an internal move
// apart from a file drop from the desktop: only the latter carries "Files".
const DRAG_ITEMS = "application/x-dashboard-items";

interface Clipboard {
  mode: "copy" | "cut";
  paths: string[];
}

/** The three layouts a desktop file manager offers. */
type ViewMode = "icons" | "list" | "compact";
type SortKey = "name" | "type" | "size" | "modified";
type SortDir = "asc" | "desc";

const VIEW_STORAGE_KEY = "dash.files.view";
const SORT_STORAGE_KEY = "dash.files.sort";

const VIEW_MODES: { id: ViewMode; label: string; Icon: (p: { width?: number; height?: number }) => ReactNode }[] = [
  { id: "icons", label: "Tampilan ikon", Icon: IconViewGrid },
  { id: "list", label: "Tampilan daftar", Icon: IconViewList },
  { id: "compact", label: "Tampilan ringkas", Icon: IconViewCompact },
];

const SORT_LABELS: Record<SortKey, string> = {
  name: "Nama",
  type: "Jenis",
  size: "Ukuran",
  modified: "Diubah",
};

// Locale-aware and numeric, so "berkas2" comes before "berkas10" and the case of
// a name never decides the order. This is what a Linux file manager does.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

// Kinds are grouped the way a desktop file manager groups them: folders first,
// then by what the file actually is, not by its raw extension order.
const KIND_LABELS = [
  "Folder",
  "Gambar",
  "Video",
  "Audio",
  "Dokumen",
  "Arsip",
  "Kode",
  "Teks",
  "Lainnya",
] as const;
type KindLabel = (typeof KIND_LABELS)[number];

const KIND_BY_EXT: Record<string, KindLabel> = {
  png: "Gambar", jpg: "Gambar", jpeg: "Gambar", gif: "Gambar", webp: "Gambar", bmp: "Gambar",
  ico: "Gambar", avif: "Gambar", heic: "Gambar", tif: "Gambar", tiff: "Gambar", svg: "Gambar",
  mp4: "Video", mkv: "Video", mov: "Video", avi: "Video", webm: "Video", m4v: "Video", mpg: "Video",
  mp3: "Audio", wav: "Audio", flac: "Audio", ogg: "Audio", oga: "Audio", m4a: "Audio", aac: "Audio",
  opus: "Audio",
  pdf: "Dokumen", doc: "Dokumen", docx: "Dokumen", xls: "Dokumen", xlsx: "Dokumen", ppt: "Dokumen",
  pptx: "Dokumen", odt: "Dokumen", ods: "Dokumen", odp: "Dokumen", rtf: "Dokumen", epub: "Dokumen",
  zip: "Arsip", tar: "Arsip", gz: "Arsip", tgz: "Arsip", bz2: "Arsip", xz: "Arsip", zst: "Arsip",
  "7z": "Arsip", rar: "Arsip", deb: "Arsip", rpm: "Arsip", jar: "Arsip", iso: "Arsip",
  go: "Kode", js: "Kode", mjs: "Kode", cjs: "Kode", jsx: "Kode", ts: "Kode", tsx: "Kode",
  py: "Kode", rb: "Kode", php: "Kode", java: "Kode", kt: "Kode", kts: "Kode", c: "Kode", h: "Kode",
  cpp: "Kode", cc: "Kode", hpp: "Kode", cs: "Kode", rs: "Kode", sh: "Kode", bash: "Kode",
  zsh: "Kode", fish: "Kode", ps1: "Kode", sql: "Kode", html: "Kode", htm: "Kode", css: "Kode",
  scss: "Kode", sass: "Kode", less: "Kode", json: "Kode", jsonc: "Kode", xml: "Kode", yml: "Kode",
  yaml: "Kode", toml: "Kode", ini: "Kode", conf: "Kode", cfg: "Kode", service: "Kode",
  lua: "Kode", pl: "Kode", swift: "Kode", dart: "Kode", vue: "Kode", svelte: "Kode", env: "Kode",
  mk: "Kode", makefile: "Kode", dockerfile: "Kode", patch: "Kode", diff: "Kode",
  txt: "Teks", md: "Teks", markdown: "Teks", log: "Teks", csv: "Teks", tsv: "Teks",
  rst: "Teks", nfo: "Teks", srt: "Teks", vtt: "Teks", license: "Teks", readme: "Teks",
};

/** fileKind answers what a file is, in the same buckets a desktop shows. */
function fileKind(entry: FileEntry): KindLabel {
  if (entry.isDir) return "Folder";
  const dot = entry.name.lastIndexOf(".");
  if (dot <= 0 || dot === entry.name.length - 1) {
    // Extension-less names still get a kind when the name itself says it.
    const lower = entry.name.toLowerCase();
    if (lower === "makefile" || lower === "dockerfile") return "Kode";
    if (lower === "readme" || lower === "license") return "Teks";
    return "Lainnya";
  }
  return KIND_BY_EXT[entry.name.slice(dot + 1).toLowerCase()] ?? "Lainnya";
}

/** kindTone is the CSS modifier that colours an icon for a kind. */
function kindTone(entry: FileEntry): string {
  switch (fileKind(entry)) {
    case "Folder":
      return "folder";
    case "Gambar":
      return "image";
    case "Video":
      return "video";
    case "Audio":
      return "audio";
    case "Dokumen":
      return "doc";
    case "Arsip":
      return "archive";
    case "Kode":
      return "code";
    case "Teks":
      return "text";
    default:
      return "other";
  }
}

/** KindGlyph draws the glyph a desktop file manager would show for an entry. */
function KindGlyph({ entry, size }: { entry: FileEntry; size: number }): ReactNode {
  const props = { width: size, height: size };
  switch (fileKind(entry)) {
    case "Folder":
      return <IconFolder {...props} />;
    case "Gambar":
      return <IconImage {...props} />;
    case "Video":
      return <IconVideo {...props} />;
    case "Audio":
      return <IconAudio {...props} />;
    case "Dokumen":
      return <IconFile {...props} />;
    case "Arsip":
      return <IconArchive {...props} />;
    case "Kode":
      return <IconCode {...props} />;
    case "Teks":
      return <IconText {...props} />;
    default:
      return <IconFile {...props} />;
  }
}

function kindRank(entry: FileEntry): number {
  return KIND_LABELS.indexOf(fileKind(entry));
}

function timeOf(iso: string): number {
  const value = Date.parse(iso);
  return Number.isNaN(value) ? 0 : value;
}

/**
 * sortEntries orders a listing the way a Linux file manager does: folders stay
 * on top in both directions, and within a group the chosen key decides, with a
 * natural name comparison as the tiebreak.
 */
function sortEntries(list: FileEntry[], sort: { key: SortKey; dir: SortDir }): FileEntry[] {
  const sorted = [...list];
  sorted.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    let primary = 0;
    switch (sort.key) {
      case "size":
        primary = a.size - b.size;
        break;
      case "modified":
        primary = timeOf(a.modified) - timeOf(b.modified);
        break;
      case "type":
        primary = kindRank(a) - kindRank(b);
        break;
      default:
        primary = 0;
    }
    const result = primary !== 0 ? primary : collator.compare(a.name, b.name);
    return sort.dir === "desc" ? -result : result;
  });
  return sorted;
}

function readStoredView(): ViewMode {
  try {
    const raw = localStorage.getItem(VIEW_STORAGE_KEY);
    if (raw === "icons" || raw === "list" || raw === "compact") return raw;
  } catch {
    // Storage disabled: fall back to the default.
  }
  return "list";
}

function readStoredSort(): { key: SortKey; dir: SortDir } {
  try {
    const raw = localStorage.getItem(SORT_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as { key?: string; dir?: string };
      const keys: SortKey[] = ["name", "type", "size", "modified"];
      const key = keys.find((candidate) => candidate === parsed.key) ?? "name";
      return { key, dir: parsed.dir === "desc" ? "desc" : "asc" };
    }
  } catch {
    // Ignore malformed or unavailable storage.
  }
  return { key: "name", dir: "asc" };
}

export function FilesPage() {
  const toast = useToast();
  const [searchParams, setSearchParams] = useSearchParams();
  const rawPath = searchParams.get("path");
  const currentPath = rawPath ? normalizePath(rawPath) : "/root";
  const [pathInput, setPathInput] = useState(currentPath);

  const [listing, setListing] = useState<FileListing | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  // Selection follows the Google Drive model: one click picks a row, a double
  // click opens it, Ctrl/Cmd toggles and Shift extends from the anchor.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [cursor, setCursor] = useState<string | null>(null);
  const anchorRef = useRef<string | null>(null);

  // Layout and order are a user preference, so they survive a reload.
  const [view, setView] = useState<ViewMode>(readStoredView);
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>(readStoredSort);

  const [createKind, setCreateKind] = useState<"file" | "folder" | null>(null);
  const [renaming, setRenaming] = useState<FileEntry | null>(null);
  const [deleting, setDeleting] = useState<FileEntry[] | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [propsEntry, setPropsEntry] = useState<FileEntry | null>(null);
  const [permsEntry, setPermsEntry] = useState<FileEntry | null>(null);
  // One picker serves "copy to", "move to" and "extract into": they all need a
  // destination folder and a confirmation.
  const [picker, setPicker] = useState<{
    kind: "copy" | "move" | "unzip";
    paths: string[];
    initialDir: string;
  } | null>(null);
  const [pickerBusy, setPickerBusy] = useState(false);
  const [clipboard, setClipboard] = useState<Clipboard | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  // Uploads: the batch keeps the directory it was started in so navigating away
  // mid-transfer can never retarget the files.
  const [batch, setBatch] = useState<{ dir: string; files: File[] } | null>(null);
  const [dragging, setDragging] = useState(false);
  const [maxUpload, setMaxUpload] = useState(512 << 20);

  const { menu, openMenu, openMenuAt, closeMenu } = useMenu();
  const abortRef = useRef<AbortController | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);

  useEffect(() => {
    // Only needed to warn about oversized files before they are sent.
    api
      .stats()
      .then((stats) => stats.maxUploadBytes > 0 && setMaxUpload(stats.maxUploadBytes))
      .catch(() => undefined);
  }, []);

  const navigate = useCallback(
    (path: string) => {
      const next = new URLSearchParams(searchParams);
      next.set("path", normalizePath(path));
      setSearchParams(next);
    },
    [searchParams, setSearchParams],
  );

  useEffect(() => {
    setPathInput(currentPath);
  }, [currentPath]);

  // A new directory means a new list: nothing from the old one is selectable.
  useEffect(() => {
    setSelected(new Set());
    setCursor(null);
    setDropTarget(null);
    anchorRef.current = null;
  }, [currentPath]);

  const load = useCallback(async () => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setLoading(true);
    setError("");
    try {
      const data = await api.listFiles(currentPath, controller.signal);
      setListing(data);
    } catch (err) {
      if ((err as Error).name === "AbortError") return;
      setError(err instanceof Error ? err.message : "Gagal memuat direktori.");
      setListing(null);
    } finally {
      setLoading(false);
    }
  }, [currentPath]);

  useEffect(() => {
    void load();
    return () => abortRef.current?.abort();
  }, [load]);

  const crumbs: Crumb[] = [{ label: "/", path: "/" }];
  const pathParts = currentPath.split("/").filter(Boolean);
  pathParts.forEach((part, index) => {
    crumbs.push({ label: part, path: `/${pathParts.slice(0, index + 1).join("/")}` });
  });

  const entries = listing?.entries ?? [];

  useEffect(() => {
    try {
      localStorage.setItem(VIEW_STORAGE_KEY, view);
    } catch {
      // Storage disabled: the choice just does not persist.
    }
  }, [view]);

  useEffect(() => {
    try {
      localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify(sort));
    } catch {
      // Storage disabled: the choice just does not persist.
    }
  }, [sort]);

  // Every consumer below works on the ordered list, so selection ranges,
  // keyboard movement and rendering can never disagree about the order.
  const sortedEntries = useMemo(() => sortEntries(entries, sort), [entries, sort]);

  const selectedEntries = useMemo(
    () => sortedEntries.filter((entry) => selected.has(entry.path)),
    [sortedEntries, selected],
  );
  // Names already in the directory: the upload dialog uses them to ask about
  // overwriting before a single byte is sent.
  const existingNames = useMemo(() => new Set(entries.map((entry) => entry.name)), [listing]);

  // A one-line summary, like the status bar of a desktop file manager.
  const summary = useMemo(() => {
    let folders = 0;
    let bytes = 0;
    for (const entry of sortedEntries) {
      if (entry.isDir) folders += 1;
      else bytes += entry.size;
    }
    const files = sortedEntries.length - folders;
    const parts = [`${sortedEntries.length} item`];
    if (folders > 0) parts.push(`${folders} folder`);
    if (files > 0) parts.push(`${files} berkas · ${formatBytes(bytes)}`);
    return parts.join(" · ");
  }, [sortedEntries]);

  // Plain-text selection summary for the status bar, in the spirit of a desktop
  // file manager: a count and a size, no card, no buttons.
  const selectionText = useMemo(() => {
    if (selectedEntries.length === 0) return "";
    const folders = selectedEntries.filter((entry) => entry.isDir).length;
    const files = selectedEntries.length - folders;
    const bytes = selectedEntries.reduce((sum, entry) => sum + (entry.isDir ? 0 : entry.size), 0);
    const detail: string[] = [];
    if (folders > 0) detail.push(`${folders} folder`);
    if (files > 0) detail.push(`${files} berkas · ${formatBytes(bytes)}`);
    return [`${selectedEntries.length} item dipilih`, detail.join(" · ")]
      .filter(Boolean)
      .join(" — ");
  }, [selectedEntries]);

  // Heading counts for the "Jenis" order.
  const kindCounts = useMemo(() => {
    const counts = new Map<KindLabel, number>();
    for (const entry of sortedEntries) {
      const kind = fileKind(entry);
      counts.set(kind, (counts.get(kind) ?? 0) + 1);
    }
    return counts;
  }, [sortedEntries]);

  const selectOnly = useCallback((path: string) => {
    setSelected(new Set([path]));
    setCursor(path);
    anchorRef.current = path;
  }, []);

  const toggleSelect = useCallback((path: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
    setCursor(path);
    anchorRef.current = path;
  }, []);

  const selectRange = useCallback(
    (path: string) => {
      const from = sortedEntries.findIndex((entry) => entry.path === anchorRef.current);
      const to = sortedEntries.findIndex((entry) => entry.path === path);
      if (from < 0 || to < 0) {
        selectOnly(path);
        return;
      }
      const [start, end] = from <= to ? [from, to] : [to, from];
      setSelected(new Set(sortedEntries.slice(start, end + 1).map((entry) => entry.path)));
      setCursor(path);
    },
    [selectOnly, sortedEntries],
  );

  const clearSelection = useCallback(() => {
    setSelected(new Set());
    setCursor(null);
    anchorRef.current = null;
  }, []);

  const selectAll = useCallback(() => {
    setSelected(new Set(sortedEntries.map((entry) => entry.path)));
  }, [sortedEntries]);

  const openEntry = useCallback(
    (entry: FileEntry) => {
      if (entry.isDir) {
        navigate(entry.path);
        return;
      }
      if (entry.editable) {
        setEditing(entry.path);
        return;
      }
      // Nothing to preview, so the file opens like it would from a desktop: the
      // browser downloads it.
      downloadSelection([entry]);
      toast.info(`"${entry.name}" diunduh.`);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [navigate, toast],
  );

  const copyPath = useCallback(
    async (values: string[]) => {
      try {
        await navigator.clipboard.writeText(values.join("\n"));
        toast.success(values.length > 1 ? `${values.length} path disalin.` : "Path disalin.");
      } catch {
        toast.error("Gagal menyalin ke clipboard.");
      }
    },
    [toast],
  );

  const copyToClipboard = useCallback(
    (mode: "copy" | "cut", paths: string[]) => {
      if (paths.length === 0) return;
      setClipboard({ mode, paths });
      // Also put the paths on the system clipboard: pasting into a terminal is
      // the most common reason to copy something here.
      if (mode === "copy") {
        void navigator.clipboard?.writeText(paths.join("\n")).catch(() => undefined);
      }
      toast.info(
        mode === "copy"
          ? paths.length > 1
            ? `${paths.length} item disalin. Tekan Ctrl+V untuk menempel.`
            : `"${baseName(paths[0])}" disalin. Tekan Ctrl+V untuk menempel.`
          : `${paths.length} item siap dipindahkan. Tekan Ctrl+V.`,
      );
    },
    [toast],
  );

  const handleDelete = async () => {
    const targets = deleting ?? [];
    if (targets.length === 0) return;
    setDeleteBusy(true);
    const failed: string[] = [];
    for (const entry of targets) {
      try {
        await api.deleteEntry(entry.path);
      } catch {
        failed.push(entry.name);
      }
    }
    setDeleteBusy(false);
    setDeleting(null);
    if (failed.length > 0) {
      toast.error(`${failed.length} item gagal dihapus: ${failed.slice(0, 3).join(", ")}`);
    }
    if (targets.length > failed.length) {
      toast.success(
        targets.length > 1
          ? `${targets.length - failed.length} item dihapus.`
          : `"${targets[0].name}" dihapus.`,
      );
    }
    clearSelection();
    await load();
  };

  const startUpload = (files: File[]) => {
    if (files.length === 0) return;
    setBatch({ dir: currentPath, files });
  };

  const pickFiles = (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? []);
    // Allow picking the same file again later.
    event.target.value = "";
    startUpload(files);
  };

  const openInTerminal = useCallback(
    (dir: string) => {
      // A floating window instead of a page change: the shell opens on top of
      // the file manager and keeps running while the dashboard is navigated.
      const opened = floatingTerminals.open({ dir, label: dir });
      if (!opened) {
        toast.error(
          `Maksimal ${MAX_FLOAT_WINDOWS} jendela terminal. Tutup salah satu dulu lalu coba lagi.`,
        );
      }
    },
    [toast],
  );

  const moveInto = useCallback(
    async (dir: string, paths: string[]) => {
      const target = normalizePath(dir);
      const moving = paths.filter((path) => parentPath(path) !== target && path !== target);
      if (moving.length === 0) return;
      const failed: string[] = [];
      for (const path of moving) {
        try {
          await api.moveEntry(path, joinPath(target, baseName(path)));
        } catch (err) {
          failed.push(`${baseName(path)}: ${describeError(err)}`);
        }
      }
      if (failed.length > 0) {
        toast.error(failed.slice(0, 2).join(" • "));
      } else {
        toast.success(
          moving.length > 1
            ? `${moving.length} item dipindahkan ke ${target}.`
            : `"${baseName(moving[0])}" dipindahkan ke ${target}.`,
        );
      }
      await load();
    },
    [load, toast],
  );

  const pasteInto = useCallback(
    async (dir: string) => {
      if (!clipboard) return;
      const target = normalizePath(dir);
      const names = new Set<string>();
      try {
        const targetListing = await api.listFiles(target);
        for (const entry of targetListing.entries) names.add(entry.name);
      } catch (err) {
        toast.error(describeError(err));
        return;
      }

      const failed: string[] = [];
      let done = 0;
      for (const path of clipboard.paths) {
        try {
          if (clipboard.mode === "cut") {
            if (parentPath(path) === target) continue;
            await api.moveEntry(path, joinPath(target, baseName(path)));
          } else {
            const name = uniqueName(names, baseName(path));
            names.add(name);
            await api.copyEntry(path, joinPath(target, name), false);
          }
          done += 1;
        } catch (err) {
          failed.push(`${baseName(path)}: ${describeError(err)}`);
        }
      }

      if (clipboard.mode === "cut") setClipboard(null);
      if (failed.length > 0) toast.error(failed.slice(0, 2).join(" • "));
      if (done > 0) {
        toast.success(
          clipboard.mode === "cut"
            ? `${done} item dipindahkan ke ${target}.`
            : `${done} item disalin ke ${target}.`,
        );
      }
      await load();
    },
    [clipboard, load, toast],
  );

  // The picker covers copy/move/extract: it collects a destination folder (or a
  // new folder name) and then runs the matching API call.
  const runPicker = async (destDir: string) => {
    if (!picker) return;
    setPickerBusy(true);
    try {
      if (picker.kind === "unzip") {
        const result = await api.unzipEntry(picker.paths[0], destDir, false);
        toast.success(`${result.entries} entri diekstrak ke ${destDir}.`);
      } else if (picker.kind === "copy") {
        const names = new Set<string>();
        try {
          const targetListing = await api.listFiles(destDir);
          for (const entry of targetListing.entries) names.add(entry.name);
        } catch {
          // A destination that does not exist yet is fine: copy creates it.
        }
        for (const path of picker.paths) {
          const name = uniqueName(names, baseName(path));
          names.add(name);
          await api.copyEntry(path, joinPath(destDir, name), false);
        }
        toast.success(
          picker.paths.length > 1
            ? `${picker.paths.length} item disalin ke ${destDir}.`
            : `"${baseName(picker.paths[0])}" disalin ke ${destDir}.`,
        );
      } else {
        for (const path of picker.paths) {
          await api.moveEntry(path, joinPath(destDir, baseName(path)));
        }
        toast.success(
          picker.paths.length > 1
            ? `${picker.paths.length} item dipindahkan ke ${destDir}.`
            : `"${baseName(picker.paths[0])}" dipindahkan ke ${destDir}.`,
        );
      }
      setPicker(null);
      clearSelection();
      await load();
    } catch (err) {
      toast.error(describeError(err));
    } finally {
      setPickerBusy(false);
    }
  };

  // ---- menus -----------------------------------------------------------

  const itemMenu = (entry: FileEntry): MenuItem[] => {
    const targets = selected.has(entry.path) ? selectedEntries : [entry];
    const single = targets.length === 1 ? targets[0] : null;
    const files = targets.filter((item) => !item.isDir);
    const zippable = single ? true : files.length === targets.length;
    const archiveDefault = single
      ? joinPath(parentPath(single.path), `${single.name.replace(/\.zip$/i, "")}-extract`)
      : "";
    return [
      {
        id: "open",
        label: single?.isDir ? "Buka" : "Edit",
        icon: single?.isDir ? <IconFolder width={15} height={15} /> : <IconEdit width={15} height={15} />,
        disabled: !single || (!single.isDir && !single.editable),
        onSelect: () => single && openEntry(single),
      },
      {
        id: "download",
        label: targets.length === 1 && files.length === 1 ? "Unduh" : `Unduh sebagai ZIP`,
        icon: <IconDownload width={15} height={15} />,
        disabled: !zippable || targets.length === 0,
        onSelect: () => downloadSelection(targets),
      },
      {
        id: "terminal",
        label: single?.isDir ? "Buka di Terminal" : "Terminal di folder ini",
        icon: <IconTerminal width={15} height={15} />,
        onSelect: () =>
          openInTerminal(single?.isDir ? single.path : parentPath(targets[0]?.path ?? currentPath)),
      },
      {
        id: "rename",
        label: "Ganti nama",
        shortcut: "F2",
        icon: <IconEdit width={15} height={15} />,
        disabled: !single,
        separated: true,
        onSelect: () => single && setRenaming(single),
      },
      {
        id: "copy",
        label: "Salin",
        shortcut: "Ctrl C",
        icon: <IconCopy width={15} height={15} />,
        onSelect: () => copyToClipboard("copy", targets.map((item) => item.path)),
      },
      {
        id: "cut",
        label: "Potong",
        shortcut: "Ctrl X",
        icon: <IconCopy width={15} height={15} />,
        onSelect: () => copyToClipboard("cut", targets.map((item) => item.path)),
      },
      {
        id: "copy-to",
        label: "Salin ke folder…",
        icon: <IconFolderUp width={15} height={15} />,
        onSelect: () =>
          setPicker({
            kind: "copy",
            paths: targets.map((item) => item.path),
            initialDir: currentPath,
          }),
      },
      {
        id: "move-to",
        label: "Pindahkan ke folder…",
        icon: <IconFolderUp width={15} height={15} />,
        onSelect: () =>
          setPicker({
            kind: "move",
            paths: targets.map((item) => item.path),
            initialDir: currentPath,
          }),
      },
      {
        id: "unzip",
        label: "Ekstrak…",
        icon: <IconFile width={15} height={15} />,
        disabled: !single || single.isDir || !/\.zip$/i.test(single.name),
        onSelect: () =>
          single &&
          setPicker({ kind: "unzip", paths: [single.path], initialDir: archiveDefault }),
      },
      {
        id: "copy-path",
        label: "Salin path",
        icon: <IconCopy width={15} height={15} />,
        separated: true,
        onSelect: () => void copyPath(targets.map((item) => item.path)),
      },
      {
        id: "perms",
        label: "Izin…",
        icon: <IconLock width={15} height={15} />,
        disabled: !single,
        onSelect: () => single && setPermsEntry(single),
      },
      {
        id: "props",
        label: "Properti",
        icon: <IconInfo width={15} height={15} />,
        disabled: !single,
        onSelect: () => single && setPropsEntry(single),
      },
      {
        id: "delete",
        label: targets.length > 1 ? `Hapus ${targets.length} item` : "Hapus",
        shortcut: "Del",
        tone: "danger",
        icon: <IconTrash width={15} height={15} />,
        separated: true,
        onSelect: () => setDeleting(targets),
      },
    ];
  };

  const areaMenu = (): MenuItem[] => [
    {
      id: "new-file",
      label: "File baru",
      icon: <IconPlus width={15} height={15} />,
      onSelect: () => setCreateKind("file"),
    },
    {
      id: "new-folder",
      label: "Folder baru",
      icon: <IconFolder width={15} height={15} />,
      onSelect: () => setCreateKind("folder"),
    },
    {
      id: "upload",
      label: "Unggah berkas",
      icon: <IconUpload width={15} height={15} />,
      onSelect: () => fileInputRef.current?.click(),
    },
    {
      id: "paste",
      label:
        clipboard && clipboard.paths.length > 0
          ? clipboard.mode === "cut"
            ? `Pindahkan ${clipboard.paths.length} item ke sini`
            : `Tempel ${clipboard.paths.length} item di sini`
          : "Tempel",
      shortcut: "Ctrl V",
      icon: <IconCopy width={15} height={15} />,
      disabled: !clipboard || clipboard.paths.length === 0,
      onSelect: () => void pasteInto(currentPath),
    },
    {
      id: "select-all",
      label: "Pilih semua",
      shortcut: "Ctrl A",
      disabled: entries.length === 0,
      separated: true,
      onSelect: selectAll,
    },
    {
      id: "refresh",
      label: "Segarkan",
      icon: <IconRefresh width={15} height={15} />,
      onSelect: () => void load(),
    },
    {
      id: "terminal",
      label: "Buka di Terminal",
      icon: <IconTerminal width={15} height={15} />,
      onSelect: () => openInTerminal(currentPath),
    },
    {
      id: "copy-path",
      label: "Salin path folder",
      icon: <IconCopy width={15} height={15} />,
      onSelect: () => void copyPath([currentPath]),
    },
  ];

  const onRowContextMenu = (entry: FileEntry, event: ReactMouseEvent) => {
    // Right-clicking outside the selection acts on that row only, like Drive.
    if (!selected.has(entry.path)) selectOnly(entry.path);
    openMenu(event, itemMenu(entry));
  };

  // ---- keyboard --------------------------------------------------------

  const dialogOpen =
    menu !== null ||
    renaming !== null ||
    deleting !== null ||
    createKind !== null ||
    propsEntry !== null ||
    permsEntry !== null ||
    picker !== null ||
    batch !== null ||
    editing !== null;

  const focusRow = (path: string) => {
    document.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`)?.focus();
  };

  const openMenuForCursor = useCallback(
    (path: string | null) => {
      if (!path) return;
      const entry = sortedEntries.find((item) => item.path === path);
      if (!entry) return;
      const node = document.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`);
      const rect = node?.getBoundingClientRect();
      // Anchored to the row so the menu follows it if the list scrolls.
      openMenuAt(rect ? rect.left + 72 : 24, rect ? rect.bottom - 6 : 24, itemMenu(entry), node);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sortedEntries, openMenuAt, selected, selectedEntries, clipboard, currentPath],
  );

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (dialogOpen) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable='true']")) return;

      const mod = event.ctrlKey || event.metaKey;
      const single = selectedEntries.length === 1 ? selectedEntries[0] : null;
      const key = event.key.toLowerCase();

      if (mod && key === "a") {
        event.preventDefault();
        selectAll();
      } else if (mod && key === "c" && selectedEntries.length > 0) {
        event.preventDefault();
        copyToClipboard("copy", selectedEntries.map((entry) => entry.path));
      } else if (mod && key === "x" && selectedEntries.length > 0) {
        event.preventDefault();
        copyToClipboard("cut", selectedEntries.map((entry) => entry.path));
      } else if (mod && key === "v" && clipboard) {
        event.preventDefault();
        void pasteInto(currentPath);
      } else if (event.key === "Escape") {
        clearSelection();
      } else if (event.key === "F2" && single) {
        event.preventDefault();
        setRenaming(single);
      } else if (event.key === "Delete" && selectedEntries.length > 0) {
        event.preventDefault();
        setDeleting(selectedEntries);
      } else if (event.key === "Enter" && single) {
        event.preventDefault();
        openEntry(single);
      } else if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
        event.preventDefault();
        openMenuForCursor(cursor ?? selectedEntries[0]?.path ?? null);
      } else if (
        event.key === "ArrowDown" ||
        event.key === "ArrowUp" ||
        event.key === "ArrowLeft" ||
        event.key === "ArrowRight"
      ) {
        if (sortedEntries.length === 0) return;
        event.preventDefault();
        const current = sortedEntries.findIndex((entry) => entry.path === cursor);
        const step = event.key === "ArrowDown" || event.key === "ArrowRight" ? 1 : -1;
        const next = Math.min(
          sortedEntries.length - 1,
          Math.max(0, current < 0 ? 0 : current + step),
        );
        selectOnly(sortedEntries[next].path);
        focusRow(sortedEntries[next].path);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    clipboard,
    clearSelection,
    copyToClipboard,
    currentPath,
    cursor,
    dialogOpen,
    openEntry,
    openMenuForCursor,
    pasteInto,
    selectAll,
    selectOnly,
    selectedEntries,
    sortedEntries,
  ]);

  // ---- drag & drop -----------------------------------------------------

  const onDragEnter = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };

  const onDragOver = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
  };

  const onDragLeave = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return;
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };

  const onDrop = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    const { files, skipped } = droppedFiles(event.dataTransfer);
    if (skipped > 0) {
      toast.error(`${skipped} folder dilewati. Kompres folder terlebih dahulu untuk mengunggahnya.`);
    }
    startUpload(files);
  };

  // Dragging rows onto a folder moves them there.
  const onRowDragStart = (entry: FileEntry, event: DragEvent<HTMLElement>) => {
    const paths = selected.has(entry.path) ? selectedEntries.map((item) => item.path) : [entry.path];
    event.dataTransfer.setData(DRAG_ITEMS, JSON.stringify(paths));
    event.dataTransfer.effectAllowed = "move";
  };

  const onFolderDragOver = (folder: FileEntry, event: DragEvent<HTMLElement>) => {
    if (!event.dataTransfer.types.includes(DRAG_ITEMS)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    if (dropTarget !== folder.path) setDropTarget(folder.path);
  };

  const onFolderDrop = (folder: FileEntry, event: DragEvent<HTMLElement>) => {
    const payload = event.dataTransfer.getData(DRAG_ITEMS);
    setDropTarget(null);
    if (!payload) return;
    event.preventDefault();
    event.stopPropagation();
    let paths: string[] = [];
    try {
      paths = JSON.parse(payload) as string[];
    } catch {
      return;
    }
    void moveInto(folder.path, paths);
  };

  const deletable = deleting ?? [];

  // ---- layouts ---------------------------------------------------------

  // Shared behaviour for every representation of an item, so a tile, a row and
  // a compact line can never drift apart.
  const itemInteractions = (entry: FileEntry) => ({
    draggable: true,
    onDragStart: (event: DragEvent<HTMLElement>) => onRowDragStart(entry, event),
    onDragEnd: () => setDropTarget(null),
    onDragOver: entry.isDir
      ? (event: DragEvent<HTMLElement>) => onFolderDragOver(entry, event)
      : undefined,
    onDragLeave: entry.isDir ? () => setDropTarget(null) : undefined,
    onDrop: entry.isDir ? (event: DragEvent<HTMLElement>) => onFolderDrop(entry, event) : undefined,
    onContextMenu: (event: ReactMouseEvent) => onRowContextMenu(entry, event),
    onClick: (event: ReactMouseEvent) => {
      if (event.shiftKey) selectRange(entry.path);
      else if (event.ctrlKey || event.metaKey) toggleSelect(entry.path);
      else selectOnly(entry.path);
    },
    onDoubleClick: () => openEntry(entry),
  });

  // Layouts without the four inline action buttons get a "…" button that opens
  // exactly the same item menu, anchored to the button.
  const moreButton = (entry: FileEntry) => (
    <button
      type="button"
      className="file-more"
      tabIndex={-1}
      title="Aksi"
      aria-label={`Aksi untuk ${entry.name}`}
      onClick={(event) => {
        event.stopPropagation();
        if (!selected.has(entry.path)) selectOnly(entry.path);
        const rect = event.currentTarget.getBoundingClientRect();
        openMenuAt(rect.right, rect.bottom + 4, itemMenu(entry), event.currentTarget);
      }}
    >
      <IconMore width={16} height={16} />
    </button>
  );

  const itemFor = (entry: FileEntry): ReactNode => {
    const isSelected = selected.has(entry.path);
    const openable = entry.isDir || entry.editable;
    const interactions = itemInteractions(entry);
    const tabIndex = cursor === entry.path ? 0 : -1;

    if (view === "icons") {
      return (
        <div
          key={entry.path}
          className={[
            "file-tile",
            isSelected ? "file-tile--selected" : "",
            dropTarget === entry.path ? "file-tile--drop" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          role="option"
          data-path={entry.path}
          aria-selected={isSelected}
          tabIndex={tabIndex}
          title={entry.name}
          {...interactions}
        >
          <span className={`file-tile__glyph file-tile__glyph--${kindTone(entry)}`}>
            <KindGlyph entry={entry} size={40} />
            {isSelected && (
              <span className="file-tile__check" aria-hidden="true">
                <IconCheck width={12} height={12} />
              </span>
            )}
          </span>
          <span className="file-tile__name">
            {entry.name}
            {entry.isLink && <span className="tag">symlink</span>}
          </span>
          <span className="file-tile__meta">{entry.isDir ? "Folder" : formatBytes(entry.size)}</span>
          {moreButton(entry)}
        </div>
      );
    }

    if (view === "compact") {
      return (
        <div
          key={entry.path}
          className={[
            "file-compact__row",
            isSelected ? "file-compact__row--selected" : "",
            dropTarget === entry.path ? "file-compact__row--drop" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          role="option"
          data-path={entry.path}
          aria-selected={isSelected}
          tabIndex={tabIndex}
          title={entry.name}
          {...interactions}
        >
          <span className={`file-icon file-icon--${kindTone(entry)}`}>
            <KindGlyph entry={entry} size={15} />
          </span>
          <span className={`file-compact__name ${openable ? "" : "file-compact__name--muted"}`}>
            {entry.name}
            {entry.isLink && <span className="tag">symlink</span>}
          </span>
          <span className="file-compact__kind">{fileKind(entry)}</span>
          <span className="file-compact__size">{entry.isDir ? "-" : formatBytes(entry.size)}</span>
          <span className="file-compact__time">{formatDateTime(entry.modified)}</span>
          {moreButton(entry)}
        </div>
      );
    }

    return (
      <div
        key={entry.path}
        className={[
          "file-row",
          isSelected ? "file-row--selected" : "",
          dropTarget === entry.path ? "file-row--drop" : "",
        ]
          .filter(Boolean)
          .join(" ")}
        role="row"
        data-path={entry.path}
        aria-selected={isSelected}
        tabIndex={tabIndex}
        {...interactions}
      >
        <span role="cell" className="file-row__name">
          <button
            type="button"
            className={`file-row__select ${isSelected ? "file-row__select--on" : ""}`}
            aria-label={isSelected ? `Batalkan pilih ${entry.name}` : `Pilih ${entry.name}`}
            tabIndex={-1}
            onClick={(event) => {
              event.stopPropagation();
              toggleSelect(entry.path);
            }}
          >
            <span className="file-row__select-box" aria-hidden="true" />
          </button>
          <span className={`file-icon file-icon--${kindTone(entry)}`}>
            <KindGlyph entry={entry} size={18} />
          </span>
          <span className={`file-row__label ${openable ? "" : "file-row__label--muted"}`}>
            {entry.name}
            {entry.isLink && <span className="tag">symlink</span>}
          </span>
        </span>
        <span role="cell" className="col-size">
          {entry.isDir ? "-" : formatBytes(entry.size)}
        </span>
        <span role="cell" className="col-modified">
          {formatDateTime(entry.modified)}
        </span>
        <span role="cell" className="col-actions">
          {!entry.isDir && (
            <IconButton label="Edit" onClick={() => setEditing(entry.path)} disabled={!entry.editable}>
              <IconEdit />
            </IconButton>
          )}
          <IconButton label="Ganti nama" onClick={() => setRenaming(entry)}>
            <IconEdit />
          </IconButton>
          {!entry.isDir && (
            <a
              className="icon-btn icon-btn--ghost"
              href={fileDownloadURL(entry.path)}
              download={entry.name}
              title="Unduh"
              aria-label={`Unduh ${entry.name}`}
            >
              <IconDownload />
            </a>
          )}
          <IconButton label="Hapus" variant="danger" onClick={() => setDeleting([entry])}>
            <IconTrash />
          </IconButton>
        </span>
      </div>
    );
  };

  // Ordering by kind gets a small group heading, so the arrangement reads like
  // a desktop file manager instead of one flat list.
  const renderItems = (): ReactNode[] => {
    const nodes: ReactNode[] = [];
    const grouped = sort.key === "type";
    let lastKind: KindLabel | null = null;
    for (const entry of sortedEntries) {
      const kind = fileKind(entry);
      if (grouped && kind !== lastKind) {
        lastKind = kind;
        nodes.push(
          <div className="file-group" key={`group-${kind}`}>
            <span className="file-group__name">{kind}</span>
            <span className="file-group__count">{kindCounts.get(kind) ?? 0}</span>
          </div>,
        );
      }
      nodes.push(itemFor(entry));
    }
    return nodes;
  };

  const headerButton = (key: SortKey, label: string) => (
    <button
      type="button"
      className={`file-sort ${sort.key === key ? "file-sort--on" : ""}`}
      title={`Urutkan menurut ${label.toLowerCase()}`}
      onClick={() =>
        setSort((prev) =>
          prev.key === key
            ? { key, dir: prev.dir === "asc" ? "desc" : "asc" }
            : { key, dir: "asc" },
        )
      }
    >
      <span>{label}</span>
      {sort.key === key &&
        (sort.dir === "asc" ? (
          <IconSortAsc width={13} height={13} />
        ) : (
          <IconSortDesc width={13} height={13} />
        ))}
    </button>
  );

  return (
    <div
      className="page page--files"
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <input
        ref={fileInputRef}
        className="files-upload-input"
        type="file"
        multiple
        tabIndex={-1}
        aria-hidden="true"
        onChange={pickFiles}
      />
      <form
        className="files-pathbar"
        onSubmit={(event) => {
          event.preventDefault();
          navigate(pathInput);
        }}
      >
        <input
          className="files-pathbar__input"
          value={pathInput}
          onChange={(event) => setPathInput(event.target.value)}
          spellCheck={false}
          autoComplete="off"
          placeholder="/root"
          aria-label="Lokasi direktori"
        />
        <Button size="sm" variant="secondary" type="submit">
          Buka
        </Button>
      </form>

      <div className="files-toolbar">
        <Breadcrumb crumbs={crumbs} onNavigate={navigate} />

        <div className="files-toolbar__actions">
          <IconButton
            label="Ke direktori induk"
            onClick={() => navigate(parentPath(currentPath))}
            disabled={currentPath === "/"}
          >
            <IconFolderUp />
          </IconButton>
          <IconButton label="Segarkan" onClick={() => void load()}>
            <IconRefresh />
          </IconButton>
          <IconButton
            label="Buka terminal di folder ini"
            onClick={() => openInTerminal(currentPath)}
          >
            <IconTerminal />
          </IconButton>
          <Button
            size="sm"
            variant="secondary"
            icon={<IconPlus width={16} height={16} />}
            onClick={() => setCreateKind("folder")}
          >
            Folder
          </Button>
          <Button
            size="sm"
            variant="secondary"
            icon={<IconPlus width={16} height={16} />}
            onClick={() => setCreateKind("file")}
          >
            File
          </Button>
          <Button
            size="sm"
            variant="primary"
            icon={<IconUpload width={16} height={16} />}
            onClick={() => fileInputRef.current?.click()}
          >
            Unggah
          </Button>
        </div>
      </div>

      <div className="files-viewbar">
        <div className="files-viewbar__controls">
          <label className="files-sortfield">
            <span className="files-sortfield__label">Urutkan</span>
            <select
              className="files-sortfield__select"
              value={sort.key}
              onChange={(event) =>
                setSort((prev) => ({ ...prev, key: event.target.value as SortKey }))
              }
              aria-label="Urutkan berkas"
            >
              {(Object.keys(SORT_LABELS) as SortKey[]).map((key) => (
                <option key={key} value={key}>
                  {SORT_LABELS[key]}
                </option>
              ))}
            </select>
          </label>
          <IconButton
            label={sort.dir === "asc" ? "Urutan menaik" : "Urutan menurun"}
            onClick={() =>
              setSort((prev) => ({ ...prev, dir: prev.dir === "asc" ? "desc" : "asc" }))
            }
          >
            {sort.dir === "asc" ? <IconSortAsc /> : <IconSortDesc />}
          </IconButton>
          <div className="files-viewswitch" role="group" aria-label="Mode tampilan">
            {VIEW_MODES.map(({ id, label, Icon }) => (
              <button
                key={id}
                type="button"
                className={`files-viewswitch__btn ${view === id ? "is-on" : ""}`}
                aria-pressed={view === id}
                title={label}
                aria-label={label}
                onClick={() => setView(id)}
              >
                <Icon width={16} height={16} />
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* One context menu covers both the rows and the empty area behind them. */}
      <div
        className="files-area"
        onContextMenu={(event) => openMenu(event, areaMenu())}
        onClick={(event) => event.target === event.currentTarget && clearSelection()}
      >
        {loading && !listing ? (
          <Spinner label="Memuat direktori..." />
        ) : error ? (
          <ErrorState message={error} onRetry={load} />
        ) : sortedEntries.length === 0 ? (
          <EmptyState
            icon={<IconFolder width={28} height={28} />}
            title="Direktori kosong"
            description="Klik kanan di area ini untuk membuat file atau folder, atau unggah berkas dari laptop."
            action={
              <div className="empty-actions">
                <Button
                  variant="primary"
                  onClick={() => fileInputRef.current?.click()}
                  icon={<IconUpload width={16} height={16} />}
                >
                  Unggah berkas
                </Button>
                <Button
                  variant="secondary"
                  onClick={() => setCreateKind("file")}
                  icon={<IconPlus width={16} height={16} />}
                >
                  Buat file
                </Button>
              </div>
            }
          />
        ) : view === "list" ? (
          <div className="file-table" role="grid" aria-label="Daftar berkas" aria-multiselectable="true">
            <div className="file-table__head" role="row">
              <span role="columnheader">{headerButton("name", "Nama")}</span>
              <span role="columnheader" className="col-size">
                {headerButton("size", "Ukuran")}
              </span>
              <span role="columnheader" className="col-modified">
                {headerButton("modified", "Diubah")}
              </span>
              <span role="columnheader" className="col-actions">
                Aksi
              </span>
            </div>
            {renderItems()}
          </div>
        ) : view === "compact" ? (
          <div
            className="file-compact"
            role="listbox"
            aria-label="Daftar berkas"
            aria-multiselectable="true"
          >
            {renderItems()}
          </div>
        ) : (
          <div
            className="file-grid"
            role="listbox"
            aria-label="Daftar berkas"
            aria-multiselectable="true"
            onClick={(event) => event.target === event.currentTarget && clearSelection()}
          >
            {renderItems()}
          </div>
        )}

        {listing?.truncated && (
          <p className="muted">
            Daftar dipotong pada {sortedEntries.length} dari {listing.totalCount} entri.
          </p>
        )}
      </div>

      {/* Desktop-style status bar: plain text, pinned above the footer. */}
      {!error && listing && (
        <div className={`files-status ${selected.size > 0 ? "files-status--active" : ""}`} role="status">
          <span className="files-status__text">{selectionText || summary}</span>
          {selected.size > 0 && (
            <span className="files-status__hint">Klik kanan untuk aksi · Esc membatalkan</span>
          )}
        </div>
      )}

      <ContextMenu state={menu} onClose={closeMenu} />

      <NameDialog
        open={createKind !== null}
        title={createKind === "folder" ? "Folder baru" : "File baru"}
        label={createKind === "folder" ? "Nama folder" : "Nama file"}
        initial=""
        confirmLabel="Buat"
        onClose={() => setCreateKind(null)}
        onSubmit={async (name) => {
          const kind = createKind ?? "file";
          const created = await api.createEntry(joinPath(currentPath, name), kind);
          toast.success(`${kind === "folder" ? "Folder" : "File"} "${name}" dibuat.`);
          setCreateKind(null);
          await load();
          selectOnly(created.path);
        }}
      />

      <NameDialog
        open={renaming !== null}
        title="Ganti nama"
        label="Nama baru"
        initial={renaming?.name ?? ""}
        confirmLabel="Simpan"
        onClose={() => setRenaming(null)}
        onSubmit={async (name) => {
          if (!renaming) return;
          const target = joinPath(parentPath(renaming.path), name);
          const renamed = await api.renameEntry(renaming.path, target);
          toast.success("Nama diperbarui.");
          setRenaming(null);
          await load();
          selectOnly(renamed.path);
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={deletable.length > 1 ? `Hapus ${deletable.length} item?` : "Hapus item ini?"}
        message={
          deletable.length > 1 ? (
            <>
              <strong>{deletable.length} item</strong> akan dihapus, termasuk seluruh isi folder yang
              dipilih. Tindakan ini tidak dapat dibatalkan.
            </>
          ) : (
            <>
              <strong>{deletable[0]?.name}</strong> akan dihapus
              {deletable[0]?.isDir ? " beserta seluruh isinya" : ""}. Tindakan ini tidak dapat
              dibatalkan.
            </>
          )
        }
        confirmLabel="Hapus"
        destructive
        busy={deleteBusy}
        onCancel={() => setDeleting(null)}
        onConfirm={handleDelete}
      />

      <FolderPickerDialog
        open={picker !== null}
        title={
          picker?.kind === "move"
            ? "Pindahkan ke folder"
            : picker?.kind === "copy"
              ? "Salin ke folder"
              : "Ekstrak arsip"
        }
        confirmLabel={picker?.kind === "move" ? "Pindahkan" : picker?.kind === "copy" ? "Salin" : "Ekstrak"}
        startDir={picker?.initialDir ?? currentPath}
        subject={picker?.paths.map((path) => baseName(path)).join(", ") ?? ""}
        busy={pickerBusy}
        onClose={() => setPicker(null)}
        onSubmit={(dest) => void runPicker(dest)}
      />

      <PermissionsDialog
        entry={permsEntry}
        onClose={() => setPermsEntry(null)}
        onSubmit={async (mode) => {
          if (!permsEntry) return;
          await api.chmodEntry(permsEntry.path, mode);
          toast.success(`Izin "${permsEntry.name}" diubah ke ${mode}.`);
          setPermsEntry(null);
          await load();
        }}
      />

      <PropertiesDialog entry={propsEntry} onClose={() => setPropsEntry(null)} />

      <FileEditor path={editing} onClose={() => setEditing(null)} onSaved={() => void load()} />

      <UploadDialog
        open={batch !== null}
        dir={batch?.dir ?? currentPath}
        files={batch?.files ?? NO_FILES}
        existing={existingNames}
        maxBytes={maxUpload}
        onClose={() => setBatch(null)}
        onUploaded={() => void load()}
      />

      {dragging && (
        <div className="files-drop" aria-hidden="true">
          <IconUpload width={30} height={30} />
          <strong>Lepaskan berkas untuk diunggah</strong>
          <span>ke {currentPath}</span>
        </div>
      )}
    </div>
  );
}

// A stable empty list keeps the upload dialog from resetting on re-render.
const NO_FILES: File[] = [];

/** describeError turns an API failure into a short Indonesian sentence. */
function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return "nama sudah dipakai di tujuan";
    if (err.status === 403) return "tidak diizinkan";
    if (err.status === 404) return "tidak ditemukan";
    return err.message || `gagal (${err.status})`;
  }
  return err instanceof Error ? err.message : "gagal";
}

/**
 * downloadSelection streams one file directly, or packs everything into a ZIP.
 * Directories only exist as archives, and several files in one archive mean one
 * download prompt instead of a burst of them.
 */
function downloadSelection(list: FileEntry[]) {
  if (list.length === 0) return;
  if (list.length === 1 && !list[0].isDir) {
    triggerDownload(fileDownloadURL(list[0].path), list[0].name);
    return;
  }
  const name = list.length === 1 ? `${list[0].name}.zip` : `pilihan-${list.length}-item.zip`;
  triggerDownload(
    archiveDownloadURL(
      list.map((entry) => entry.path),
      name,
    ),
    name,
  );
}

function triggerDownload(href: string, filename: string) {
  const link = document.createElement("a");
  link.href = href;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
}

/**
 * uniqueName picks a free name in a directory, the way Drive does: "berkas.txt"
 * becomes "berkas (2).txt" rather than overwriting anything.
 */
function uniqueName(taken: Set<string>, name: string): string {
  if (!taken.has(name)) return name;
  const dot = name.lastIndexOf(".");
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : "";
  for (let i = 2; i < 1000; i += 1) {
    const candidate = `${stem} (${i})${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
  return `${stem} (${Date.now()})${ext}`;
}

function hasFiles(event: DragEvent<HTMLElement>): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}

// A dropped folder arrives as a File that has no content; uploading it would
// create an empty file, so directories are filtered out and reported instead.
function droppedFiles(dataTransfer: DataTransfer): { files: File[]; skipped: number } {
  const items = Array.from(dataTransfer.items ?? []);
  if (items.length === 0) {
    return { files: Array.from(dataTransfer.files ?? []), skipped: 0 };
  }
  const files: File[] = [];
  let skipped = 0;
  for (const item of items) {
    if (item.kind !== "file") continue;
    const entry = (
      item as unknown as {
        webkitGetAsEntry?: () => { isDirectory: boolean } | null;
      }
    ).webkitGetAsEntry?.();
    if (entry?.isDirectory) {
      skipped += 1;
      continue;
    }
    const file = item.getAsFile();
    if (file) files.push(file);
  }
  return { files, skipped };
}

/**
 * FolderPickerDialog collects a destination folder. Only directories are shown
 * (that is what is being chosen), and an optional name creates a new folder
 * inside the one on screen.
 */
function FolderPickerDialog({
  open,
  title,
  confirmLabel,
  startDir,
  subject,
  busy,
  onClose,
  onSubmit,
}: {
  open: boolean;
  title: string;
  confirmLabel: string;
  startDir: string;
  subject: string;
  busy: boolean;
  onClose: () => void;
  onSubmit: (destDir: string) => void;
}) {
  const [dir, setDir] = useState(normalizePath(startDir));
  const [listing, setListing] = useState<FileListing | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [newName, setNewName] = useState("");

  useEffect(() => {
    if (open) {
      setDir(normalizePath(startDir));
      setNewName("");
    }
  }, [open, startDir]);

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoading(true);
    setError("");
    api
      .listFiles(dir)
      .then((data) => active && setListing(data))
      .catch((err) => active && setError(describeError(err)))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [open, dir]);

  const folders = (listing?.entries ?? []).filter((entry) => entry.isDir);
  const trimmed = newName.trim();
  const dest = trimmed ? joinPath(dir, trimmed) : dir;

  return (
    <Modal
      open={open}
      title={title}
      onClose={busy ? () => undefined : onClose}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={() => onSubmit(dest)} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {subject && <p className="picker__subject">{subject}</p>}

      <div className="picker__path">
        <IconButton
          label="Ke folder induk"
          onClick={() => setDir(parentPath(dir))}
          disabled={dir === "/"}
        >
          <IconChevronLeft />
        </IconButton>
        <code>{dir}</code>
      </div>

      <div className="picker__list" role="listbox" aria-label="Daftar folder">
        {loading && !listing ? (
          <Spinner label="Memuat folder..." />
        ) : error ? (
          <ErrorState message={error} />
        ) : folders.length === 0 ? (
          <p className="muted picker__empty">Tidak ada subfolder di sini.</p>
        ) : (
          folders.map((folder) => (
            <button
              key={folder.path}
              type="button"
              className="picker__item"
              role="option"
              aria-selected={false}
              onClick={() => setDir(folder.path)}
            >
              <IconFolder width={16} height={16} />
              <span>{folder.name}</span>
            </button>
          ))
        )}
      </div>

      <label className="field">
        <span>Folder baru di dalam {dir} (opsional)</span>
        <input
          value={newName}
          onChange={(event) => setNewName(event.target.value)}
          placeholder="biarkan kosong untuk memakai folder yang dipilih"
          spellCheck={false}
        />
      </label>

      <p className="muted picker__dest">
        Tujuan: <code>{dest}</code>
      </p>
    </Modal>
  );
}

/**
 * PermissionsDialog edits the nine permission bits. Setuid, setgid and sticky
 * are not offered at all, and the backend refuses them even if they are typed
 * into the octal field.
 */
function PermissionsDialog({
  entry,
  onClose,
  onSubmit,
}: {
  entry: FileEntry | null;
  onClose: () => void;
  onSubmit: (mode: string) => Promise<void>;
}) {
  const [octal, setOctal] = useState("644");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (entry) {
      setOctal(parsePermString(entry.mode).toString(8).padStart(3, "0"));
      setError("");
      setBusy(false);
    }
  }, [entry]);

  const bits = octalToBits(octal);
  const toggle = (index: number) => {
    const next = bits ^ (1 << index);
    setOctal((next & 0o777).toString(8).padStart(3, "0"));
  };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await onSubmit(octal);
    } catch (err) {
      setError(describeError(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      open={entry !== null}
      title="Izin berkas"
      onClose={busy ? () => undefined : onClose}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={submit} loading={busy}>
            Simpan izin
          </Button>
        </>
      }
    >
      <p className="muted">
        <code>{entry?.path}</code>
      </p>

      <div className="perms">
        {["Pemilik", "Grup", "Lainnya"].map((who, row) => (
          <div className="perms__row" key={who}>
            <span className="perms__who">{who}</span>
            {["r", "w", "x"].map((flag, col) => {
              const bit = 8 - (row * 3 + col);
              const on = (bits & (1 << bit)) !== 0;
              return (
                <label key={flag} className={`perms__toggle ${on ? "perms__toggle--on" : ""}`}>
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={() => toggle(bit)}
                    disabled={busy}
                  />
                  <span>{flag}</span>
                </label>
              );
            })}
          </div>
        ))}
      </div>

      <label className="field">
        <span>Oktal (maksimal 0777)</span>
        <input
          value={octal}
          onChange={(event) => {
            const cleaned = event.target.value.replace(/[^0-7]/g, "").slice(0, 3);
            setOctal(cleaned);
          }}
          spellCheck={false}
          inputMode="numeric"
        />
      </label>

      <p className="muted">
        Bit setuid, setgid, dan sticky tidak tersedia: dashboard berjalan sebagai root, jadi bit itu
        akan memberi siapa pun yang bisa mengeksekusi berkas tersebut hak akses yang sama.
      </p>

      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}

// parsePermString turns a mode string such as "-rwxr-xr-x" into its 9 bits.
function parsePermString(mode: string): number {
  const flags = mode.slice(-9);
  if (flags.length < 9) return 0o644;
  const order = "rwxrwxrwx";
  let bits = 0;
  for (let i = 0; i < 9; i += 1) {
    if (flags[i] === order[i]) bits |= 1 << (8 - i);
  }
  return bits;
}

function octalToBits(octal: string): number {
  const value = Number.parseInt(octal, 8);
  return Number.isFinite(value) ? value & 0o777 : 0;
}

/** PropertiesDialog loads the detailed stat for one path on demand. */
function PropertiesDialog({ entry, onClose }: { entry: FileEntry | null; onClose: () => void }) {
  const [stat, setStat] = useState<FileStat | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!entry) return;
    let active = true;
    setStat(null);
    setError("");
    api
      .statEntry(entry.path)
      .then((data) => active && setStat(data))
      .catch((err) => active && setError(describeError(err)));
    return () => {
      active = false;
    };
  }, [entry]);

  const info = stat ?? entry;

  return (
    <Modal open={entry !== null} title="Properti" onClose={onClose} size="sm">
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <dl className="info-list">
        <div>
          <dt>Nama</dt>
          <dd>{info?.name}</dd>
        </div>
        <div>
          <dt>Path</dt>
          <dd className="mono">{info?.path}</dd>
        </div>
        <div>
          <dt>Jenis</dt>
          <dd>
            {info?.isDir ? "Folder" : "Berkas"}
            {info?.isLink ? " (symlink)" : ""}
            {stat?.linkTarget ? ` → ${stat.linkTarget}` : ""}
          </dd>
        </div>
        <div>
          <dt>Ukuran</dt>
          <dd>{info?.isDir ? "-" : formatBytes(info?.size ?? 0)}</dd>
        </div>
        <div>
          <dt>Izin</dt>
          <dd className="mono">{info?.mode}</dd>
        </div>
        {stat && (
          <>
            <div>
              <dt>Pemilik</dt>
              <dd>
                {stat.owner} ({stat.uid}) : {stat.group} ({stat.gid})
              </dd>
            </div>
            <div>
              <dt>Inode</dt>
              <dd className="mono">{stat.inode}</dd>
            </div>
            <div>
              <dt>Hard link</dt>
              <dd>{stat.links}</dd>
            </div>
          </>
        )}
        <div>
          <dt>Diubah</dt>
          <dd>{info ? formatDateTime(info.modified) : ""}</dd>
        </div>
        <div>
          <dt>Dapat diedit</dt>
          <dd>{info?.editable ? "Ya" : "Tidak"}</dd>
        </div>
      </dl>
    </Modal>
  );
}

interface NameDialogProps {
  open: boolean;
  title: string;
  label: string;
  initial: string;
  confirmLabel: string;
  onClose: () => void;
  onSubmit: (name: string) => Promise<void>;
}

function NameDialog({ open, title, label, initial, confirmLabel, onClose, onSubmit }: NameDialogProps) {
  const [value, setValue] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [fieldError, setFieldError] = useState("");

  useEffect(() => {
    if (open) {
      setValue(initial);
      setFieldError("");
    }
  }, [open, initial]);

  const submit = async () => {
    const trimmed = value.trim();
    if (!trimmed) {
      setFieldError("Nama tidak boleh kosong.");
      return;
    }
    if (trimmed.includes("/") || trimmed === "." || trimmed === "..") {
      setFieldError("Nama tidak boleh berisi '/' atau berupa '.'/'..'.");
      return;
    }
    setBusy(true);
    setFieldError("");
    try {
      await onSubmit(trimmed);
    } catch (err) {
      setFieldError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title={title}
      onClose={busy ? () => undefined : onClose}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!value.trim()}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <label className="field">
        <span>{label}</span>
        <input
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void submit()}
          autoFocus
        />
      </label>
      {fieldError && (
        <p className="form-error" role="alert">
          {fieldError}
        </p>
      )}
    </Modal>
  );
}

function FileEditor({
  path,
  onClose,
  onSaved,
}: {
  path: string | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [content, setContent] = useState<FileContent | null>(null);
  const [value, setValue] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!path) return;
    let active = true;
    setLoading(true);
    setError("");
    setContent(null);
    setDirty(false);
    api
      .readFile(path)
      .then((data) => {
        if (!active) return;
        setContent(data);
        setValue(data.binary ? "" : data.content);
      })
      .catch((err) => active && setError(err instanceof Error ? err.message : "Gagal membaca file."))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [path]);

  const save = async () => {
    if (!path) return;
    setSaving(true);
    try {
      await api.writeFile(path, value);
      toast.success("File disimpan.");
      setDirty(false);
      onSaved();
      onClose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal menyimpan file.");
    } finally {
      setSaving(false);
    }
  };

  const requestClose = () => {
    if (dirty && !window.confirm("Perubahan belum disimpan. Tutup tanpa menyimpan?")) {
      return;
    }
    onClose();
  };

  return (
    <Modal
      open={path !== null}
      title={path ? `Edit: ${baseName(path)}` : "Edit file"}
      onClose={requestClose}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={requestClose} disabled={saving}>
            Tutup
          </Button>
          <Button
            variant="primary"
            onClick={save}
            loading={saving}
            disabled={loading || !!error || content?.binary || content?.readOnly || !dirty}
          >
            Simpan
          </Button>
        </>
      }
    >
      {loading ? (
        <Spinner label="Membaca file..." />
      ) : error ? (
        <ErrorState message={error} />
      ) : content?.binary ? (
        <EmptyState title="File biner" description="File ini tidak dapat ditampilkan sebagai teks." />
      ) : (
        <>
          <div className="editor-meta">
            <span>{baseName(path ?? "")}</span>
            <span>{formatBytes(content?.size ?? 0)}</span>
            <span>{content?.language}</span>
            {content?.readOnly && <span className="tag">read-only</span>}
          </div>
          <textarea
            className="editor"
            value={value}
            spellCheck={false}
            readOnly={content?.readOnly}
            onChange={(e) => {
              setValue(e.target.value);
              setDirty(true);
            }}
          />
        </>
      )}
    </Modal>
  );
}
