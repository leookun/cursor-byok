import type { Provider } from "../api";
import type { ProviderPreset } from "../utils/providerPresets";
import styles from "./ProviderPresetGallery.module.scss";

interface Props {
  presets: ProviderPreset[];
  providers: Provider[];
  onPick: (preset: ProviderPreset) => void;
}

export function ProviderPresetGallery({ presets, providers, onPick }: Props) {
  const existing = new Set(providers.map((provider) => provider.name));
  return (
    <div className={styles.gallery}>
      {presets.map((preset) => {
        const added = preset.matchName ? existing.has(preset.matchName) : existing.has(preset.draft.name);
        return (
          <button
            key={preset.key}
            type="button"
            className={styles.card}
            data-added={added || undefined}
            title={preset.keyHint}
            onClick={() => onPick(preset)}
          >
            <span className={styles.iconWrap}>
              <img className={styles.icon} src={preset.icon} alt={preset.name} />
            </span>
            <span className={styles.body}>
              <span className={styles.titleRow}>
                <span className={styles.name}>{preset.name}</span>
                <span className={styles.typeBadge}>{preset.providerType === "anthropic" ? "Anthropic 协议" : "OpenAI 协议"}</span>
                {added && <span className={styles.addedBadge}>{t("已配置")}</span>}
              </span>
              <span className={styles.description}>{preset.description}</span>
              <span className={styles.modelRow}>
                {preset.models.map((entry) => (
                  <span key={entry.model_id} className={styles.modelChip}>{entry.model_id}</span>
                ))}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}
