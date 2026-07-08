<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { getAdRuntime, openAdExternalURL } from "@/services/clientApi";

const OPEN_AD_EVENT = "cursor:open-ad";
const BRIDGE_SOURCE = "cursor-ad";

const visible = ref(false);
const runtimeState = ref(null);
const iframeSrc = ref("");
const viewport = ref({
  width: typeof window === "undefined" ? 1024 : window.innerWidth,
  height: typeof window === "undefined" ? 768 : window.innerHeight,
});

const showingHashes = new Set();
let refreshPending = false;
let hideTimer = 0;

const frameStyle = computed(() => {
  const win = runtimeState.value?.window ?? {};
  const maxWidth = Math.max(220, viewport.value.width - 32);
  const maxHeight = Math.max(160, viewport.value.height - 32);
  const width = Math.min(clampNumber(win.width, 280, 1200, 640), maxWidth);
  const height = Math.min(clampNumber(win.height, 180, 900, 420), maxHeight);
  return {
    width: `${width}px`,
    height: `${height}px`,
    maxWidth: "calc(100vw - 32px)",
    maxHeight: "calc(100vh - 32px)",
  };
});

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function asBoolean(value) {
  return value === true || value === "true" || value === 1 || value === "1";
}

function asNumber(value, fallback = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function clampNumber(value, min, max, fallback) {
  const parsed = asNumber(value, fallback);
  return Math.min(max, Math.max(min, parsed || fallback));
}

function normalizeRuntime(source, preferredSlotId = "") {
  const raw = source && typeof source === "object" ? source : {};
  const slots = Array.isArray(raw.slots) ? raw.slots : [];
  const selectedSlot =
    slots.find((slot) => asString(slot?.id) === asString(preferredSlotId)) ||
    slots[0] ||
    raw;
  const slot = selectedSlot && typeof selectedSlot === "object" ? selectedSlot : {};
  const win = raw.window && typeof raw.window === "object" ? raw.window : {};
  const slotWin = slot.window && typeof slot.window === "object" ? slot.window : win;
  return {
    id: asString(slot.id) || asString(preferredSlotId) || "1",
    available: asBoolean(slot.available),
    enabled: asBoolean(slot.enabled),
    packageHash: asString(slot.packageHash),
    assetBaseURL: asString(slot.assetBaseURL).replace(/\/+$/, ""),
    indexURL: asString(slot.indexURL),
    window: {
      width: Math.round(asNumber(slotWin.width, 640)),
      height: Math.round(asNumber(slotWin.height, 420)),
    },
  };
}

function expectedAdOrigin() {
  const baseURL = runtimeState.value?.assetBaseURL;
  if (!baseURL) {
    return "";
  }
  try {
    return new URL(baseURL).origin;
  } catch (_error) {
    return "";
  }
}

function canOpen(runtime) {
  if (!runtime?.available || !runtime.enabled) {
    return false;
  }
  return Boolean(runtime.packageHash && runtime.assetBaseURL);
}

async function openCurrentAd(slotId = "") {
  if (visible.value || refreshPending) {
    return;
  }
  refreshPending = true;
  try {
    const nextRuntime = normalizeRuntime(await getAdRuntime(), slotId);
    runtimeState.value = nextRuntime;
    if (canOpen(nextRuntime)) {
      await showAd(nextRuntime);
    }
  } catch (_error) {
    // 广告入口失败不影响主界面。
  } finally {
    refreshPending = false;
  }
}

async function showAd(runtime) {
  const hash = runtime.packageHash;
  if (showingHashes.has(hash)) {
    return;
  }
  showingHashes.add(hash);
  try {
    const indexURL = runtime.indexURL || `${runtime.assetBaseURL}/index.html`;
    const separator = indexURL.includes("?") ? "&" : "?";
    iframeSrc.value = `${indexURL}${separator}hash=${encodeURIComponent(hash)}&ts=${Date.now()}`;
    visible.value = true;
  } finally {
    showingHashes.delete(hash);
  }
}

function closeAd() {
  visible.value = false;
  if (hideTimer) {
    window.clearTimeout(hideTimer);
  }
  hideTimer = window.setTimeout(() => {
    iframeSrc.value = "";
  }, 260);
}

function handleMessage(event) {
  const origin = expectedAdOrigin();
  if (origin && event.origin !== origin) {
    return;
  }
  const data = event.data && typeof event.data === "object" ? event.data : {};
  if (data.source !== BRIDGE_SOURCE) {
    return;
  }
  if (data.type === "close") {
    closeAd();
    return;
  }
  if (data.type === "openExternal") {
    const targetURL = asString(data.url);
    if (targetURL) {
      void openAdExternalURL(targetURL).catch(() => {});
    }
  }
}

function handleOpenRequested(event) {
  void openCurrentAd(asString(event?.detail?.slotId));
}

function updateViewport() {
  viewport.value = {
    width: window.innerWidth,
    height: window.innerHeight,
  };
}

onMounted(() => {
  window.addEventListener("message", handleMessage);
  window.addEventListener(OPEN_AD_EVENT, handleOpenRequested);
  window.addEventListener("resize", updateViewport);
});

onBeforeUnmount(() => {
  if (hideTimer) {
    window.clearTimeout(hideTimer);
  }
  window.removeEventListener("message", handleMessage);
  window.removeEventListener(OPEN_AD_EVENT, handleOpenRequested);
  window.removeEventListener("resize", updateViewport);
});
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="modal-mask-layer fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
      >
        <Transition name="ad-frame">
          <iframe
            v-show="visible && iframeSrc"
            :src="iframeSrc"
            :style="frameStyle"
            class="block overflow-hidden rounded-none border-none bg-transparent shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            sandbox="allow-scripts allow-forms allow-same-origin"
            title="Advertisement"
          />
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-mask-enter-active,
.modal-mask-leave-active {
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}

.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}

.ad-frame-enter-active,
.ad-frame-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}

.ad-frame-enter-from,
.ad-frame-leave-to {
  opacity: 0;
  transform: scale(0.96) translateY(-8px);
}
</style>
