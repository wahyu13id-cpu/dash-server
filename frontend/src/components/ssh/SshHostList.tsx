import { IconLock, IconPlug, IconStar } from "../Icons";
import { formatRelative } from "../../lib/format";
import { hostEndpoint } from "../../lib/ssh";
import type { SshHost } from "../../lib/types";

interface SshHostListProps {
  hosts: SshHost[];
  selected: string;
  onSelect: (alias: string) => void;
  onToggleFavorite: (host: SshHost) => void;
}

function HostRow({
  host,
  selected,
  onSelect,
  onToggleFavorite,
}: {
  host: SshHost;
  selected: boolean;
  onSelect: (alias: string) => void;
  onToggleFavorite: (host: SshHost) => void;
}) {
  const hasSecret = host.hasPassword || host.hasPassphrase;
  return (
    <li>
      <div className={`ssh-host ${selected ? "ssh-host--selected" : ""}`}>
        <button
          type="button"
          className="ssh-host__main"
          onClick={() => onSelect(host.alias)}
          aria-current={selected}
        >
          <span className="ssh-host__title">
            <IconPlug width={15} height={15} className="ssh-host__icon" />
            <span className="ssh-host__alias">{host.alias}</span>
            {hasSecret && (
              <span
                className={`ssh-host__lock ${host.secretBroken ? "ssh-host__lock--broken" : ""}`}
                title={
                  host.secretBroken
                    ? "Kredensial tersimpan tidak dapat dibuka (kunci sesi berubah)"
                    : `Tersimpan: ${[host.hasPassword ? "password" : "", host.hasPassphrase ? "passphrase kunci" : ""]
                        .filter(Boolean)
                        .join(", ")}`
                }
              >
                <IconLock width={13} height={13} />
              </span>
            )}
            {!host.writable && <span className="ssh-host__ro">hanya baca</span>}
          </span>
          <span className="ssh-host__endpoint">{hostEndpoint(host)}</span>
          <span className="ssh-host__meta">
            {host.lastConnectedAt ? `Terakhir ${formatRelative(host.lastConnectedAt)}` : "Belum pernah dibuka"}
            {host.connectCount > 0 ? ` · ${host.connectCount}×` : ""}
          </span>
        </button>
        <button
          type="button"
          className={`ssh-host__star ${host.favorite ? "is-on" : ""}`}
          aria-label={host.favorite ? `Hapus ${host.alias} dari favorit` : `Tandai ${host.alias} sebagai favorit`}
          aria-pressed={host.favorite}
          onClick={() => onToggleFavorite(host)}
        >
          <IconStar width={15} height={15} fill={host.favorite ? "currentColor" : "none"} />
        </button>
      </div>
    </li>
  );
}

export function SshHostList({ hosts, selected, onSelect, onToggleFavorite }: SshHostListProps) {
  const favorites = hosts.filter((host) => host.favorite);
  const others = hosts.filter((host) => !host.favorite);

  if (hosts.length === 0) {
    return (
      <p className="ssh-list__empty">
        Tidak ada host yang cocok. Ubah pencarian atau tambahkan host baru.
      </p>
    );
  }

  return (
    <div className="ssh-list">
      {favorites.length > 0 && (
        <>
          <div className="ssh-list__group">
            <IconStar width={13} height={13} fill="currentColor" />
            <span>Favorit</span>
          </div>
          <ul className="ssh-list__items">
            {favorites.map((host) => (
              <HostRow
                key={host.alias}
                host={host}
                selected={host.alias === selected}
                onSelect={onSelect}
                onToggleFavorite={onToggleFavorite}
              />
            ))}
          </ul>
        </>
      )}
      <div className="ssh-list__group">
        <IconPlug width={13} height={13} />
        <span>{favorites.length > 0 ? "Semua host" : "Host"} ({others.length})</span>
      </div>
      <ul className="ssh-list__items">
        {others.map((host) => (
          <HostRow
            key={host.alias}
            host={host}
            selected={host.alias === selected}
            onSelect={onSelect}
            onToggleFavorite={onToggleFavorite}
          />
        ))}
      </ul>
    </div>
  );
}
