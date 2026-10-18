import type { ReactNode } from "react";

interface CardProps {
  title?: string;
  subtitle?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  padded?: boolean;
}

export function Card({ title, subtitle, actions, children, className, padded = true }: CardProps) {
  const classes = ["card", padded ? "card--padded" : "", className].filter(Boolean).join(" ");
  return (
    <section className={classes}>
      {(title || actions) && (
        <header className="card__header">
          <div>
            {title && <h2 className="card__title">{title}</h2>}
            {subtitle && <p className="card__subtitle">{subtitle}</p>}
          </div>
          {actions && <div className="card__actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "neutral" | "success" | "warning" | "danger" | "info";
}) {
  return <span className={`badge badge--${tone}`}>{children}</span>;
}
