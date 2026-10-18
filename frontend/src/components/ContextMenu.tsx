import {
  Fragment,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

/**
 * A right-click menu.
 *
 * It is rendered through a portal into <body> and positioned `fixed`, because
 * the file table it opens from is `overflow: hidden`: a menu rendered in place
 * would be clipped by the table and trapped in its stacking context.
 */

export interface MenuItem {
  id: string;
  label: string;
  icon?: ReactNode;
  /** Rendered on the right, purely as a hint (the shortcut itself is bound elsewhere). */
  shortcut?: string;
  tone?: "default" | "danger";
  disabled?: boolean;
  /** Draws a divider above this entry. */
  separated?: boolean;
  onSelect: () => void;
}

export interface MenuState {
  x: number;
  y: number;
  items: MenuItem[];
  /**
   * The element the menu was opened from. When set, the menu follows it while
   * the page scrolls instead of vanishing, exactly like a native desktop menu
   * that stays glued to the row it belongs to.
   */
  anchor?: HTMLElement | null;
  /** Where inside the anchor the pointer was, so following keeps the same spot. */
  offsetX?: number;
  offsetY?: number;
}

const EDGE = 8;

/** Describes the element a menu should stay attached to. */
function anchorOf(target: EventTarget | null, x: number, y: number) {
  if (!(target instanceof HTMLElement) || !target.isConnected) {
    return { anchor: null, offsetX: 0, offsetY: 0 };
  }
  const rect = target.getBoundingClientRect();
  return { anchor: target, offsetX: x - rect.left, offsetY: y - rect.top };
}

/** True while the tracked point is still inside the viewport (small margin). */
function pointOnScreen(x: number, y: number): boolean {
  return (
    y >= -EDGE && y <= window.innerHeight + EDGE && x >= -EDGE && x <= window.innerWidth + EDGE
  );
}

/** useMenu keeps the "which items, where" state next to the component that opens it. */
export function useMenu() {
  const [menu, setMenu] = useState<MenuState | null>(null);

  const openMenuAt = useCallback(
    (x: number, y: number, items: MenuItem[], anchor: HTMLElement | null = null) => {
      setMenu({ x, y, items, ...anchorOf(anchor, x, y) });
    },
    [],
  );

  const openMenu = useCallback(
    (event: ReactMouseEvent, items: MenuItem[]) => {
      // Only the areas we handle lose the browser menu; everywhere else keeps it.
      event.preventDefault();
      event.stopPropagation();
      openMenuAt(event.clientX, event.clientY, items, event.currentTarget as HTMLElement | null);
    },
    [openMenuAt],
  );

  const closeMenu = useCallback(() => setMenu(null), []);

  return { menu, openMenu, openMenuAt, closeMenu };
}

export function ContextMenu({ state, onClose }: { state: MenuState | null; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: 0, top: 0 });
  const [active, setActive] = useState(-1);
  const menuId = useId();

  // Clamp inside the viewport, following the anchor when there is one. Runs
  // before paint, so the menu never flashes at the raw cursor position first.
  const place = useCallback((next: MenuState) => {
    const node = ref.current;
    if (!node) return;
    let { x, y } = next;
    const anchor = next.anchor;
    if (anchor && anchor.isConnected) {
      const rect = anchor.getBoundingClientRect();
      x = rect.left + (next.offsetX ?? 0);
      y = rect.top + (next.offsetY ?? 0);
    }
    const { width, height } = node.getBoundingClientRect();
    const maxLeft = Math.max(EDGE, window.innerWidth - width - EDGE);
    const maxTop = Math.max(EDGE, window.innerHeight - height - EDGE);
    setPos({
      left: Math.min(Math.max(EDGE, x), maxLeft),
      top: Math.min(Math.max(EDGE, y), maxTop),
    });
  }, []);

  useLayoutEffect(() => {
    if (!state) return;
    place(state);
    setActive(firstEnabled(state.items));
  }, [state, place]);

  useEffect(() => {
    if (!state) return;
    ref.current?.focus();

    const close = () => onClose();
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) onClose();
    };
    // Right-clicking elsewhere must not leave two menus stacked.
    const onContextMenu = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) onClose();
    };

    // Scrolling moves the menu with the row it belongs to instead of closing
    // it; it only closes once that row is gone or scrolled out of sight.
    let frame = 0;
    const follow = () => {
      frame = 0;
      const anchor = state.anchor;
      if (anchor) {
        if (!anchor.isConnected) {
          onClose();
          return;
        }
        const rect = anchor.getBoundingClientRect();
        const x = rect.left + (state.offsetX ?? 0);
        const y = rect.top + (state.offsetY ?? 0);
        if (!pointOnScreen(x, y)) {
          onClose();
          return;
        }
      }
      place(state);
    };
    const schedule = () => {
      if (frame) return;
      frame = requestAnimationFrame(follow);
    };

    document.addEventListener("mousedown", onPointerDown, true);
    document.addEventListener("contextmenu", onContextMenu, true);
    window.addEventListener("scroll", schedule, true);
    window.addEventListener("resize", schedule);
    window.addEventListener("blur", close);
    return () => {
      if (frame) cancelAnimationFrame(frame);
      document.removeEventListener("mousedown", onPointerDown, true);
      document.removeEventListener("contextmenu", onContextMenu, true);
      window.removeEventListener("scroll", schedule, true);
      window.removeEventListener("resize", schedule);
      window.removeEventListener("blur", close);
    };
  }, [state, onClose, place]);

  if (!state) return null;

  const step = (from: number, delta: number) => {
    const items = state.items;
    for (let i = 1; i <= items.length; i += 1) {
      const next = (from + delta * i + items.length * i) % items.length;
      if (!items[next].disabled) return next;
    }
    return from;
  };

  const onKeyDown = (event: ReactKeyboardEvent) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((i) => step(i, 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((i) => step(i, -1));
    } else if (event.key === "Home") {
      event.preventDefault();
      setActive(firstEnabled(state.items));
    } else if (event.key === "End") {
      event.preventDefault();
      setActive(lastEnabled(state.items));
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      const item = state.items[active];
      if (item && !item.disabled) {
        onClose();
        item.onSelect();
      }
    } else if (event.key === "Escape" || event.key === "Tab") {
      event.preventDefault();
      onClose();
    }
  };

  return createPortal(
    <div
      ref={ref}
      id={menuId}
      className="context-menu"
      style={{ left: pos.left, top: pos.top }}
      role="menu"
      aria-orientation="vertical"
      tabIndex={-1}
      onKeyDown={onKeyDown}
      onContextMenu={(event) => event.preventDefault()}
    >
      {state.items.map((item, index) => (
        <Fragment key={item.id}>
          {item.separated && <div className="context-menu__sep" role="separator" />}
          <button
            type="button"
            role="menuitem"
            disabled={item.disabled}
            className={[
              "context-menu__item",
              item.tone === "danger" ? "context-menu__item--danger" : "",
              index === active ? "context-menu__item--active" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onMouseMove={() => !item.disabled && setActive(index)}
            onClick={() => {
              if (item.disabled) return;
              onClose();
              item.onSelect();
            }}
          >
            <span className="context-menu__icon" aria-hidden="true">
              {item.icon}
            </span>
            <span className="context-menu__label">{item.label}</span>
            {item.shortcut && <span className="context-menu__shortcut">{item.shortcut}</span>}
          </button>
        </Fragment>
      ))}
    </div>,
    document.body,
  );
}

function firstEnabled(items: MenuItem[]): number {
  return items.findIndex((item) => !item.disabled);
}

function lastEnabled(items: MenuItem[]): number {
  for (let i = items.length - 1; i >= 0; i -= 1) {
    if (!items[i].disabled) return i;
  }
  return -1;
}
