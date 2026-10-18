import type { ReactNode } from "react";
import { IconRefresh } from "./Icons";

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="loading-block" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      {label && <span>{label}</span>}
    </div>
  );
}

export function EmptyState({
  title,
  description,
  action,
  icon,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="empty-state">
      {icon && <div className="empty-state__icon">{icon}</div>}
      <h3>{title}</h3>
      {description && <p>{description}</p>}
      {action && <div className="empty-state__action">{action}</div>}
    </div>
  );
}

export function ErrorState({
  title = "Terjadi kesalahan",
  message,
  onRetry,
}: {
  title?: string;
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="error-state" role="alert">
      <h3>{title}</h3>
      <p>{message}</p>
      {onRetry && (
        <button className="btn btn--secondary btn--sm" onClick={onRetry} type="button">
          <IconRefresh width={16} height={16} />
          <span>Coba lagi</span>
        </button>
      )}
    </div>
  );
}
