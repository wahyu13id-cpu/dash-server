import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api, ApiError } from "../lib/api";
import type { LoginResponse, User } from "../lib/types";

interface AuthState {
  user: User | null;
  loading: boolean;
  /** Verifies the password; may return a challenge that needs a second step. */
  login: (username: string, password: string) => Promise<LoginResponse>;
  /** Completes a two-step login with a TOTP or recovery code. */
  completeTotpLogin: (challenge: string, code: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const { user: me } = await api.me();
      setUser(me);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setUser(null);
      }
    }
  }, []);

  useEffect(() => {
    void refresh().finally(() => setLoading(false));
  }, [refresh]);

  const login = useCallback(async (username: string, password: string) => {
    const result = await api.login(username, password);
    if (result.status === "ok" && result.user) {
      setUser(result.user);
    }
    return result;
  }, []);

  const completeTotpLogin = useCallback(async (challenge: string, code: string) => {
    const result = await api.loginTotp(challenge, code);
    if (result.user) {
      setUser(result.user);
    }
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
    }
  }, []);

  const value = useMemo(
    () => ({ user, loading, login, completeTotpLogin, logout, refresh }),
    [user, loading, login, completeTotpLogin, logout, refresh],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}
