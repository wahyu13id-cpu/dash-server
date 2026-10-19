import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./context/AuthContext";
import { OpsProvider } from "./context/OpsContext";
import { ToastProvider } from "./context/ToastContext";
import { Layout } from "./components/Layout";
import { Toaster } from "./components/Toaster";
import { Spinner } from "./components/Feedback";
import { LoginPage } from "./pages/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import { TerminalPage } from "./pages/TerminalPage";
import { FilesPage } from "./pages/FilesPage";
import { SecurityPage } from "./pages/SecurityPage";
import { SshPage } from "./pages/SshPage";
import { ProfilePage } from "./pages/ProfilePage";
import { ServicesPage } from "./pages/ServicesPage";
import { ProcessesPage } from "./pages/ProcessesPage";
import { LogsPage } from "./pages/LogsPage";
import { PackagesPage } from "./pages/PackagesPage";

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth();
  if (loading) {
    return (
      <div className="boot-screen">
        <Spinner label="Memeriksa sesi..." />
      </div>
    );
  }
  if (!user) {
    return <Navigate to="/login" replace />;
  }
  return <>{children}</>;
}

export default function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <AuthProvider>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route
              element={
                <RequireAuth>
                  <OpsProvider>
                    <Layout />
                  </OpsProvider>
                </RequireAuth>
              }
            >
              <Route path="/" element={<DashboardPage />} />
              <Route path="/terminal" element={<TerminalPage />} />
              <Route path="/ssh" element={<SshPage />} />
              <Route path="/files" element={<FilesPage />} />
              <Route path="/services" element={<ServicesPage />} />
              <Route path="/processes" element={<ProcessesPage />} />
              <Route path="/logs" element={<LogsPage />} />
              <Route path="/packages" element={<PackagesPage />} />
              <Route path="/security" element={<SecurityPage />} />
              <Route path="/profile" element={<ProfilePage />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
          <Toaster />
        </AuthProvider>
      </ToastProvider>
    </BrowserRouter>
  );
}
