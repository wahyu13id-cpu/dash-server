import { IconChevronRight } from "./Icons";

export interface Crumb {
  label: string;
  path: string;
}

export function Breadcrumb({
  crumbs,
  onNavigate,
}: {
  crumbs: Crumb[];
  onNavigate: (path: string) => void;
}) {
  return (
    <nav className="breadcrumb" aria-label="Breadcrumb">
      {crumbs.map((crumb, index) => {
        const isLast = index === crumbs.length - 1;
        return (
          <span key={crumb.path || "root"} className="breadcrumb__item">
            {index > 0 && <IconChevronRight width={14} height={14} className="breadcrumb__sep" />}
            {isLast ? (
              <span className="breadcrumb__current" aria-current="page">
                {crumb.label}
              </span>
            ) : (
              <button className="breadcrumb__link" onClick={() => onNavigate(crumb.path)} type="button">
                {crumb.label}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}
