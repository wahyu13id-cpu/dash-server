import { useState } from "react";

import { Badge } from "../Card";
import { IconFingerprint, IconInfo, IconKey } from "../Icons";
import type { SshDefaultBlock, SshKey, SshOption } from "../../lib/types";

export function SshKeysPanel({ keys }: { keys: SshKey[] }) {
  return (
    <section className="ssh-side-panel">
      <h3 className="ssh-side-panel__title">
        <IconKey width={15} height={15} /> Kunci di <code>~/.ssh</code>
        <span className="ssh-side-panel__count">{keys.length}</span>
      </h3>
      {keys.length === 0 ? (
        <p className="muted">
          Tidak ada kunci privat. Buat dengan <code>ssh-keygen</code> lewat terminal.
        </p>
      ) : (
        <ul className="ssh-key-list">
          {keys.map((key) => (
            <li key={key.name}>
              <div className="ssh-key-list__top">
                <span className="ssh-key-list__name">{key.name}</span>
                {key.hasPublic ? (
                  <Badge tone="info">{key.type || "kunci"}</Badge>
                ) : (
                  <Badge tone="warning">tanpa .pub</Badge>
                )}
              </div>
              {key.fingerprint ? (
                <div className="ssh-key-list__fp">
                  <IconFingerprint width={13} height={13} />
                  <code>{key.fingerprint}</code>
                </div>
              ) : (
                <span className="muted">sidik jari tidak tersedia</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function OptionRows({ options }: { options: SshOption[] }) {
  if (options.length === 0) return <p className="muted">Tidak ada direktif.</p>;
  return (
    <div className="ssh-effective">
      {options.map((opt) => (
        <div className="ssh-effective__row" key={`${opt.key}-${opt.value}`}>
          <code className="ssh-effective__key">{opt.key}</code>
          <code className="ssh-effective__value">{opt.value || '""'}</code>
        </div>
      ))}
    </div>
  );
}

export function SshDefaultsPanel({ defaults }: { defaults: SshDefaultBlock[] }) {
  const [openIndex, setOpenIndex] = useState<number | null>(null);

  const label = (block: SshDefaultBlock) => {
    if (block.implied) return "Direktif global (di atas Host pertama)";
    if (block.kind === "match") return `Match ${(block.patterns ?? []).join(" ")}`;
    return `Host ${(block.patterns ?? []).join(" ")}`;
  };

  return (
    <section className="ssh-side-panel">
      <h3 className="ssh-side-panel__title">
        <IconInfo width={15} height={15} /> Blok global &amp; wildcard
        <span className="ssh-side-panel__count">{defaults.length}</span>
      </h3>
      {defaults.length === 0 ? (
        <p className="muted">
          Belum ada blok wildcard. Direktif global bisa ditambahkan langsung ke{" "}
          <code>~/.ssh/config</code>.
        </p>
      ) : (
        <ul className="ssh-defaults">
          {defaults.map((block, index) => (
            <li key={`${block.file}-${block.startLine}`}>
              <button
                type="button"
                className="ssh-defaults__head"
                onClick={() => setOpenIndex(openIndex === index ? null : index)}
                aria-expanded={openIndex === index}
              >
                <span className="ssh-defaults__label">{label(block)}</span>
                <span className="ssh-defaults__meta">
                  {(block.options ?? []).length} direktif · baris {block.startLine}–{block.endLine}
                </span>
              </button>
              {openIndex === index && (
                <div className="ssh-defaults__body">
                  <OptionRows options={block.options ?? []} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
