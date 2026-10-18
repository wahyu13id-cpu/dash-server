import type { ButtonHTMLAttributes, ReactNode } from "react";

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
}

export function Button({
  variant = "secondary",
  size = "md",
  loading = false,
  icon,
  children,
  className,
  disabled,
  ...rest
}: ButtonProps) {
  const classes = ["btn", `btn--${variant}`, `btn--${size}`, className].filter(Boolean).join(" ");
  return (
    <button className={classes} disabled={disabled || loading} {...rest}>
      {loading ? <span className="spinner spinner--sm" aria-hidden="true" /> : icon}
      {children ? <span>{children}</span> : null}
    </button>
  );
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  variant?: Variant;
  children: ReactNode;
}

export function IconButton({ label, variant = "ghost", children, className, ...rest }: IconButtonProps) {
  const classes = ["icon-btn", `icon-btn--${variant}`, className].filter(Boolean).join(" ");
  return (
    <button className={classes} title={label} aria-label={label} {...rest}>
      {children}
    </button>
  );
}
