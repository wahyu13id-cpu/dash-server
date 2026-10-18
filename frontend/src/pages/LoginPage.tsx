import {
  useEffect,
  useRef,
  useState,
  type FormHTMLAttributes,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  type Ref,
} from "react";
import { Navigate } from "react-router-dom";
import { useAuth } from "../context/AuthContext";
import { Button } from "../components/Button";
import { IconServer } from "../components/Icons";

/**
 * LoginCard renders the 3D panel: decorative light layers, then the real form
 * content on a raised layer (see the login section of global.css). The ref is
 * forwarded so the page can drive the tilt/spotlight CSS variables.
 */
function LoginCard({
  cardRef,
  children,
  ...formProps
}: { cardRef: Ref<HTMLFormElement>; children: ReactNode } & FormHTMLAttributes<HTMLFormElement>) {
  return (
    <form className="login-card" ref={cardRef} {...formProps}>
      <span className="login-card__aura" aria-hidden="true" />
      <span className="login-card__sheen" aria-hidden="true" />
      <span className="login-card__spot" aria-hidden="true" />
      <div className="login-card__content">{children}</div>
    </form>
  );
}

export function LoginPage() {
  const { user, loading, login, completeTotpLogin } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [challenge, setChallenge] = useState("");
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submittedCode = useRef("");

  const stageRef = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLFormElement>(null);
  const frame = useRef(0);

  const inSecondStep = challenge !== "";

  // Auto-submit as soon as a full six-digit code has been typed.
  useEffect(() => {
    if (!inSecondStep || useRecovery) return;
    if (/^\d{6}$/.test(code) && submittedCode.current !== code) {
      submittedCode.current = code;
      void verify(code);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code, inSecondStep, useRecovery]);

  if (loading) {
    return (
      <div className="login-page">
        <span className="spinner" aria-label="Memuat" />
      </div>
    );
  }
  if (user) {
    return <Navigate to="/" replace />;
  }

  const submitCredentials = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = await login(username.trim(), password);
      if (result.status === "totp_required" && result.challenge) {
        setChallenge(result.challenge);
        setCode("");
        setUseRecovery(false);
        submittedCode.current = "";
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal masuk.");
    } finally {
      setBusy(false);
    }
  };

  async function verify(value: string) {
    const trimmed = value.trim();
    if (!trimmed) return;
    setBusy(true);
    setError("");
    try {
      await completeTotpLogin(challenge, trimmed);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Verifikasi gagal.");
      setCode("");
      submittedCode.current = "";
      // An expired or unknown challenge cannot be retried: start over.
      if (err instanceof Error && /expired|invalid or expired/i.test(err.message)) {
        setChallenge("");
      }
    } finally {
      setBusy(false);
    }
  }

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault();
    await verify(code);
  };

  // The pointer steers the tilt and the spotlight. Values are written straight
  // to CSS variables (throttled to one animation frame) so moving the mouse
  // never re-renders React.
  const handlePointerMove = (event: ReactMouseEvent<HTMLDivElement>) => {
    const stage = stageRef.current;
    const card = cardRef.current;
    if (!stage || !card || frame.current) return;
    const { clientX, clientY } = event;
    frame.current = window.requestAnimationFrame(() => {
      frame.current = 0;
      const rect = stage.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) return;
      const px = (clientX - rect.left) / rect.width;
      const py = (clientY - rect.top) / rect.height;
      const dx = Math.min(1, Math.max(-1, (px - 0.5) * 2));
      const dy = Math.min(1, Math.max(-1, (py - 0.5) * 2));
      // The hovered side lifts towards the viewer.
      card.style.setProperty("--tilt-y", `${(-dx * 8).toFixed(2)}deg`);
      card.style.setProperty("--tilt-x", `${(dy * 6).toFixed(2)}deg`);
      card.style.setProperty("--spot-x", `${(px * 100).toFixed(1)}%`);
      card.style.setProperty("--spot-y", `${(py * 100).toFixed(1)}%`);
    });
  };

  // Dropping the variables restores the resting tilt from the stylesheet.
  const handlePointerLeave = () => {
    if (frame.current) {
      window.cancelAnimationFrame(frame.current);
      frame.current = 0;
    }
    const card = cardRef.current;
    if (!card) return;
    card.style.removeProperty("--tilt-y");
    card.style.removeProperty("--tilt-x");
    card.style.removeProperty("--spot-x");
    card.style.removeProperty("--spot-y");
  };

  return (
    <div className="login-page" onMouseMove={handlePointerMove} onMouseLeave={handlePointerLeave}>
      <div className="login-stage" ref={stageRef}>
        {inSecondStep ? (
          <LoginCard cardRef={cardRef} onSubmit={submitCode}>
            <div className="login-card__brand">
              <span className="brand__mark" aria-hidden="true">
                <IconServer width={22} height={22} />
              </span>
              <div>
                <h1>Verifikasi dua langkah</h1>
                <p>
                  {useRecovery
                    ? "Masukkan salah satu kode pemulihan Anda."
                    : "Masukkan kode 6 digit dari aplikasi autentikator Anda."}
                </p>
              </div>
            </div>

            {useRecovery ? (
              <label className="field">
                <span>Kode pemulihan</span>
                <input
                  value={code}
                  onChange={(e) => setCode(e.target.value.toUpperCase())}
                  placeholder="XXXXX-XXXXX"
                  autoComplete="one-time-code"
                  autoFocus
                  spellCheck={false}
                  maxLength={11}
                  required
                />
              </label>
            ) : (
              <label className="field">
                <span>Kode verifikasi</span>
                <input
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
                  placeholder="000000"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  autoFocus
                  className="field__code"
                  maxLength={6}
                  required
                />
              </label>
            )}

            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}

            <Button type="submit" variant="primary" loading={busy} disabled={code.trim().length < 6}>
              Verifikasi
            </Button>

            <button
              type="button"
              className="login-card__link"
              onClick={() => {
                setUseRecovery((v) => !v);
                setCode("");
                setError("");
                submittedCode.current = "";
              }}
            >
              {useRecovery ? "Gunakan kode autentikator" : "Gunakan kode pemulihan"}
            </button>

            <button
              type="button"
              className="login-card__link"
              onClick={() => {
                setChallenge("");
                setCode("");
                setError("");
                setPassword("");
              }}
            >
              Kembali
            </button>
          </LoginCard>
        ) : (
          <LoginCard cardRef={cardRef} onSubmit={submitCredentials}>
            <div className="login-card__brand">
              <span className="brand__mark" aria-hidden="true">
                <IconServer width={22} height={22} />
              </span>
              <div>
                <h1>Server Dashboard</h1>
                <p>Masuk untuk mengelola server Anda</p>
              </div>
            </div>

            <label className="field">
              <span>Nama pengguna</span>
              <input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                required
              />
            </label>

            <label className="field">
              <span>Kata sandi</span>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
              />
            </label>

            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}

            <Button type="submit" variant="primary" loading={busy} disabled={!username || !password}>
              Masuk
            </Button>
          </LoginCard>
        )}
      </div>
    </div>
  );
}
