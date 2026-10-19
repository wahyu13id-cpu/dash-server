import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../lib/api";
import type { OpsAvailability } from "../lib/types";

// The host can be missing systemd, journald, procfs or a package manager (a
// container, an unusual distro, a locked-down host). One probe answers what is
// available, so the navigation and every operations page can hide or explain
// what cannot work instead of failing on the first request.

interface OpsContextValue {
  availability: OpsAvailability | null;
  loading: boolean;
  refresh: () => void;
}

const OpsContext = createContext<OpsContextValue | null>(null);

export function OpsProvider({ children }: { children: ReactNode }) {
  const [availability, setAvailability] = useState<OpsAvailability | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    api
      .opsAvailability()
      .then((value) => {
        if (active) setAvailability(value);
      })
      .catch(() => {
        if (active) setAvailability(null);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [nonce]);

  const refresh = useCallback(() => setNonce((n) => n + 1), []);

  return (
    <OpsContext.Provider value={{ availability, loading, refresh }}>{children}</OpsContext.Provider>
  );
}

export function useOps(): OpsContextValue {
  const ctx = useContext(OpsContext);
  if (!ctx) {
    throw new Error("useOps must be used inside OpsProvider");
  }
  return ctx;
}
