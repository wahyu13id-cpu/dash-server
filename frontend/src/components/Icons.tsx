// Minimal inline SVG icon set. No third-party or brand assets are used; these
// are generic geometric glyphs.
import type { SVGProps } from "react";

type IconProps = SVGProps<SVGSVGElement>;

function Base({ children, ...props }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...props}
    >
      {children}
    </svg>
  );
}

export const IconDashboard = (p: IconProps) => (
  <Base {...p}>
    <rect x="3" y="3" width="7.5" height="7.5" rx="1.5" />
    <rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5" />
    <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5" />
    <rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5" />
  </Base>
);

export const IconTerminal = (p: IconProps) => (
  <Base {...p}>
    <rect x="2.5" y="4" width="19" height="16" rx="2" />
    <path d="M7 9.5l3 2.5-3 2.5" />
    <path d="M12.5 15h4.5" />
  </Base>
);

export const IconFolder = (p: IconProps) => (
  <Base {...p}>
    <path d="M3 7.5A2 2 0 0 1 5 5.5h3.6a2 2 0 0 1 1.6.8l.9 1.2H19a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
  </Base>
);

export const IconFile = (p: IconProps) => (
  <Base {...p}>
    <path d="M6 3h7l5 5v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" />
    <path d="M13 3v5h5" />
  </Base>
);

export const IconTrash = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 7h16" />
    <path d="M9 7V4.8A.8.8 0 0 1 9.8 4h4.4a.8.8 0 0 1 .8.8V7" />
    <path d="M6 7l1 12.2a1 1 0 0 0 1 .8h8a1 1 0 0 0 1-.8L18 7" />
    <path d="M10 11v6M14 11v6" />
  </Base>
);

export const IconEdit = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 20h4l10-10-4-4L4 16z" />
    <path d="M13.5 6.5l4 4" />
  </Base>
);

export const IconPlus = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 5v14M5 12h14" />
  </Base>
);

export const IconRefresh = (p: IconProps) => (
  <Base {...p}>
    <path d="M20 11a8 8 0 1 0-2.3 6.1" />
    <path d="M20 5v6h-6" />
  </Base>
);

export const IconLogout = (p: IconProps) => (
  <Base {...p}>
    <path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3" />
    <path d="M10 12h9" />
    <path d="M13 8l-4 4 4 4" />
  </Base>
);

export const IconClose = (p: IconProps) => (
  <Base {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </Base>
);

export const IconMenu = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 7h16M4 12h16M4 17h16" />
  </Base>
);

export const IconChevronLeft = (p: IconProps) => (
  <Base {...p}>
    <path d="M14.5 6.5L9 12l5.5 5.5" />
  </Base>
);

export const IconChevronRight = (p: IconProps) => (
  <Base {...p}>
    <path d="M9.5 6.5L15 12l-5.5 5.5" />
  </Base>
);

export const IconFolderUp = (p: IconProps) => (
  <Base {...p}>
    <path d="M3 7.5A2 2 0 0 1 5 5.5h3.6a2 2 0 0 1 1.6.8l.9 1.2H19a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    <path d="M12 16v-4M10 14l2-2 2 2" />
  </Base>
);

export const IconServer = (p: IconProps) => (
  <Base {...p}>
    <rect x="3" y="4" width="18" height="6" rx="1.6" />
    <rect x="3" y="14" width="18" height="6" rx="1.6" />
    <path d="M7 7h.01M7 17h.01" />
  </Base>
);

export const IconDownload = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 4v11" />
    <path d="M8 11l4 4 4-4" />
    <path d="M5 19h14" />
  </Base>
);

export const IconUpload = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 20V9" />
    <path d="M8 13l4-4 4 4" />
    <path d="M5 5h14" />
  </Base>
);

export const IconStop = (p: IconProps) => (
  <Base {...p}>
    <rect x="6.5" y="6.5" width="11" height="11" rx="1.5" />
  </Base>
);

export const IconKey = (p: IconProps) => (
  <Base {...p}>
    <circle cx="8" cy="12" r="3.2" />
    <path d="M11.2 12H20" />
    <path d="M17 12v3M14 12v2" />
  </Base>
);

export const IconCpu = (p: IconProps) => (
  <Base {...p}>
    <rect x="6.5" y="6.5" width="11" height="11" rx="1.8" />
    <rect x="10" y="10" width="4" height="4" rx="0.8" />
    <path d="M9.5 3.5v3M14.5 3.5v3M9.5 17.5v3M14.5 17.5v3M3.5 9.5h3M3.5 14.5h3M17.5 9.5h3M17.5 14.5h3" />
  </Base>
);

export const IconMemory = (p: IconProps) => (
  <Base {...p}>
    <rect x="3" y="7" width="18" height="10" rx="1.6" />
    <path d="M7 7v10M12 7v10M17 7v10" />
    <path d="M6 17v3M18 17v3" />
  </Base>
);

export const IconDisk = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <circle cx="12" cy="12" r="2.4" />
    <path d="M12 3.5V9M18.5 16l-4.6-2.4" />
  </Base>
);

export const IconNetwork = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M3.5 12h17" />
    <path d="M12 3.5c2.4 2.3 3.6 5.2 3.6 8.5s-1.2 6.2-3.6 8.5c-2.4-2.3-3.6-5.2-3.6-8.5S9.6 5.8 12 3.5z" />
  </Base>
);

export const IconActivity = (p: IconProps) => (
  <Base {...p}>
    <path d="M3 12h4l2.5-7 5 14L17 12h4" />
  </Base>
);

export const IconShield = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 3.2l7 2.6v5.6c0 4.3-2.9 7.6-7 9.4-4.1-1.8-7-5.1-7-9.4V5.8z" />
    <path d="M9.2 12.1l2 2 3.6-3.9" />
  </Base>
);

export const IconCopy = (p: IconProps) => (
  <Base {...p}>
    <rect x="9" y="9" width="11" height="11" rx="1.8" />
    <path d="M15 6.5V5.8A1.8 1.8 0 0 0 13.2 4H5.8A1.8 1.8 0 0 0 4 5.8v7.4A1.8 1.8 0 0 0 5.8 15h.7" />
  </Base>
);

export const IconCheck = (p: IconProps) => (
  <Base {...p}>
    <path d="M5 12.5l4.5 4.5L19 7" />
  </Base>
);

export const IconStar = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 4.2l2.4 4.9 5.4.8-3.9 3.8.9 5.4-4.8-2.6-4.8 2.6.9-5.4L4.2 9.9l5.4-.8z" />
  </Base>
);

export const IconSearch = (p: IconProps) => (
  <Base {...p}>
    <circle cx="11" cy="11" r="6.2" />
    <path d="M15.6 15.6L20 20" />
  </Base>
);

export const IconPlay = (p: IconProps) => (
  <Base {...p}>
    <path d="M8 5.5l11 6.5-11 6.5z" />
  </Base>
);

export const IconAlert = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 4.5l8.5 14.5H3.5z" />
    <path d="M12 10v4.2" />
    <path d="M12 16.6h.01" />
  </Base>
);

export const IconLock = (p: IconProps) => (
  <Base {...p}>
    <rect x="4.5" y="10.5" width="15" height="9.5" rx="1.8" />
    <path d="M8 10.5V8a4 4 0 0 1 8 0v2.5" />
    <path d="M12 14v2.6" />
  </Base>
);

export const IconPlug = (p: IconProps) => (
  <Base {...p}>
    <path d="M8.5 3.5v5M15.5 3.5v5" />
    <path d="M6 8.5h12v3.2a6 6 0 0 1-6 6 6 6 0 0 1-6-6z" />
    <path d="M12 17.7V21" />
  </Base>
);

export const IconFingerprint = (p: IconProps) => (
  <Base {...p}>
    <path d="M12 4.5c-2.4 0-4.6 1.2-5.9 3.1" />
    <path d="M5.2 10.4c-.3 1.2-.4 2.5-.4 3.8" />
    <path d="M12 8.2c-2 0-3.7 1.7-3.7 3.8 0 2.6-.3 5.2-1 7.5" />
    <path d="M12 12c0 3.2-.3 5.6-.8 7.5" />
    <path d="M15.2 19.4c.5-1.9.8-4.1.8-7.4 0-2.1-1.7-3.8-4-3.8" />
    <path d="M18.6 16.6c.6-2.3.9-4.6.6-6.9" />
    <path d="M19.2 6.4A8.6 8.6 0 0 0 12 3.5" />
  </Base>
);

export const IconArrowUpRight = (p: IconProps) => (
  <Base {...p}>
    <path d="M8 16L16.5 7.5" />
    <path d="M9.5 7.5h7v7" />
  </Base>
);

export const IconMinus = (p: IconProps) => (
  <Base {...p}>
    <path d="M5 12h14" />
  </Base>
);

export const IconInfo = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 11v5.2" />
    <path d="M12 7.8h.01" />
  </Base>
);

export const IconImage = (p: IconProps) => (
  <Base {...p}>
    <rect x="3" y="4.5" width="18" height="15" rx="2" />
    <circle cx="8.6" cy="10" r="1.6" />
    <path d="M4.2 17.4l4.4-3.8a1.2 1.2 0 0 1 1.6 0l4.4 3.9" />
    <path d="M13.6 15.1l2-1.7a1.2 1.2 0 0 1 1.6 0l2.6 2.3" />
  </Base>
);

export const IconVideo = (p: IconProps) => (
  <Base {...p}>
    <rect x="2.5" y="5" width="19" height="14" rx="2" />
    <path d="M10.2 9.6l4.6 2.4-4.6 2.4V9.6z" />
  </Base>
);

export const IconAudio = (p: IconProps) => (
  <Base {...p}>
    <path d="M9.5 17.5V6.4l8.5-1.6v10.7" />
    <circle cx="7.1" cy="17.6" r="2.4" />
    <circle cx="15.6" cy="15.5" r="2.4" />
  </Base>
);

export const IconArchive = (p: IconProps) => (
  <Base {...p}>
    <rect x="3.5" y="4" width="17" height="4.5" rx="1.2" />
    <path d="M5 8.5V19a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8.5" />
    <path d="M10.4 12.6h3.2" />
  </Base>
);

export const IconCode = (p: IconProps) => (
  <Base {...p}>
    <path d="M9 7.5L4.5 12 9 16.5" />
    <path d="M15 7.5L19.5 12 15 16.5" />
    <path d="M13.3 5.5l-2.6 13" />
  </Base>
);

export const IconText = (p: IconProps) => (
  <Base {...p}>
    <path d="M6 3h7l5 5v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" />
    <path d="M13 3v5h5" />
    <path d="M8.6 13h6.8M8.6 16.4h6.8" />
  </Base>
);

export const IconViewGrid = (p: IconProps) => (
  <Base {...p}>
    <rect x="3.5" y="3.5" width="4.6" height="4.6" rx="1" />
    <rect x="9.7" y="3.5" width="4.6" height="4.6" rx="1" />
    <rect x="15.9" y="3.5" width="4.6" height="4.6" rx="1" />
    <rect x="3.5" y="9.7" width="4.6" height="4.6" rx="1" />
    <rect x="9.7" y="9.7" width="4.6" height="4.6" rx="1" />
    <rect x="15.9" y="9.7" width="4.6" height="4.6" rx="1" />
    <rect x="3.5" y="15.9" width="4.6" height="4.6" rx="1" />
    <rect x="9.7" y="15.9" width="4.6" height="4.6" rx="1" />
    <rect x="15.9" y="15.9" width="4.6" height="4.6" rx="1" />
  </Base>
);

export const IconViewList = (p: IconProps) => (
  <Base {...p}>
    <rect x="3.5" y="4" width="4.2" height="4.2" rx="1" />
    <path d="M10.6 6.1h9.9" />
    <rect x="3.5" y="9.9" width="4.2" height="4.2" rx="1" />
    <path d="M10.6 12h9.9" />
    <rect x="3.5" y="15.8" width="4.2" height="4.2" rx="1" />
    <path d="M10.6 17.9h9.9" />
  </Base>
);

export const IconViewCompact = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 5.4h16M4 9.7h16M4 14h16M4 18.3h10.5" />
  </Base>
);

export const IconSortAsc = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 7h9M4 12h6M4 17h3" />
    <path d="M16.5 18.5V7" />
    <path d="M13.4 10.1l3.1-3.1 3.1 3.1" />
  </Base>
);

export const IconSortDesc = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 7h9M4 12h6M4 17h3" />
    <path d="M16.5 5.5V17" />
    <path d="M13.4 13.9l3.1 3.1 3.1-3.1" />
  </Base>
);

export const IconMore = (p: IconProps) => (
  <Base {...p}>
    <circle cx="5.6" cy="12" r="1.5" fill="currentColor" stroke="none" />
    <circle cx="12" cy="12" r="1.5" fill="currentColor" stroke="none" />
    <circle cx="18.4" cy="12" r="1.5" fill="currentColor" stroke="none" />
  </Base>
);

export const IconUser = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="8" r="3.6" />
    <path d="M4.8 20.2a7.4 7.4 0 0 1 14.4 0" />
  </Base>
);

export const IconEye = (p: IconProps) => (
  <Base {...p}>
    <path d="M2.5 12S6 6.5 12 6.5 21.5 12 21.5 12 18 17.5 12 17.5 2.5 12 2.5 12z" />
    <circle cx="12" cy="12" r="2.8" />
  </Base>
);

export const IconEyeOff = (p: IconProps) => (
  <Base {...p}>
    <path d="M4 4l16 16" />
    <path d="M9.6 5.3A9.5 9.5 0 0 1 12 5c6 0 9.5 7 9.5 7a17 17 0 0 1-3.3 4.2" />
    <path d="M6.4 7.2A16.6 16.6 0 0 0 2.5 12S6 19 12 19a9.6 9.6 0 0 0 3.6-.7" />
    <path d="M10 10.2a2.9 2.9 0 0 0 4 4" />
  </Base>
);

export const IconCog = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="12" r="3" />
    <path d="M12 3.5v2.2M12 18.3v2.2M4.9 7.9l1.9 1.1M17.2 15l1.9 1.1M4.9 16.1l1.9-1.1M17.2 9l1.9-1.1" />
  </Base>
);

export const IconBox = (p: IconProps) => (
  <Base {...p}>
    <path d="M20.5 7.8v8.4a1.6 1.6 0 0 1-.8 1.4l-6.9 3.7a1.6 1.6 0 0 1-1.6 0L4.3 17.6a1.6 1.6 0 0 1-.8-1.4V7.8" />
    <path d="M3.8 7.4l7.4-3.6a1.6 1.6 0 0 1 1.6 0l7.4 3.6-7.4 3.9z" />
    <path d="M12 11.3v9.7" />
  </Base>
);

export const IconScroll = (p: IconProps) => (
  <Base {...p}>
    <path d="M6.5 3.5h11a1 1 0 0 1 1 1v13a3 3 0 0 0 3 3h-13a3 3 0 0 1-3-3v-13a1 1 0 0 1 1-1z" />
    <path d="M8.5 8h7M8.5 11.5h7M8.5 15h4" />
  </Base>
);

export const IconClock = (p: IconProps) => (
  <Base {...p}>
    <circle cx="12" cy="12" r="8.6" />
    <path d="M12 7.4V12l3 1.8" />
  </Base>
);
