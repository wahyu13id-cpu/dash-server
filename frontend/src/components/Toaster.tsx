import { useToast } from "../context/ToastContext";
import { IconClose } from "./Icons";

export function Toaster() {
  const { toasts, dismiss } = useToast();
  if (toasts.length === 0) return null;

  return (
    <div className="toaster" role="region" aria-label="Notifikasi">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast--${t.kind}`} role={t.kind === "error" ? "alert" : "status"}>
          <span className="toast__message">{t.message}</span>
          <button className="toast__close" onClick={() => dismiss(t.id)} aria-label="Tutup notifikasi" type="button">
            <IconClose width={14} height={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
