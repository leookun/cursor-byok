import { reactive, watch } from "vue";

const STORAGE_KEY = "cursor-pet:settings";

const defaults = {
  enabled: true,
  scale: 0.3,
  opacity: 1.0,
  activePetId: "nezukocoder",
};

function loadSettings() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return { ...defaults, ...JSON.parse(raw) };
  } catch (_) {}
  return { ...defaults };
}

function saveSettings(settings) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(settings));
  } catch (_) {}
}

export const petSettings = reactive(loadSettings());

watch(
  () => ({ ...petSettings }),
  (val) => saveSettings(val),
  { deep: true }
);
