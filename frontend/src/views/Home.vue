<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Switch from "@/components/ui/Switch.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import { localized } from "@/i18n/runtime";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import { getAdRuntime } from "@/services/clientApi";
import {
  appState,
  appViewState,
  openConfigWindow,
  openModelConfigWindow,
  saveRoutingMode,
  syncHomeMetrics,
  syncServiceState,
  toUserError,
  toggleService,
} from "@/state/appState";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

const L = {
  serviceError: localized("a1b2c3d4e5f6006b", "Service Error"),
  serviceOpFailed: localized("a1b2c3d4e5f60071", "Service operation failed"),
  refreshFailed: localized("a1b2c3d4e5f60072", "Refresh failed"),
  openFailed: localized("a1b2c3d4e5f6006c", "Failed to open"),
  switchFailed: localized("a1b2c3d4e5f60073", "Switch failed"),
  switchedDirect: localized("a1b2c3d4e5f60029", "Switched to Direct Cursor Mode"),
  switchedLocal: localized("a1b2c3d4e5f6002a", "Switched to Local Service Mode"),
  directMode: localized("a1b2c3d4e5f60025", "Direct Mode"),
  directModeDesc: localized("a1b2c3d4e5f60026", "When enabled, Cursor connects directly to official. Do not enable."),
  directModeEnabled: localized("a1b2c3d4e5f60027", "Currently in Direct Mode"),
  directModeDisabled: localized("a1b2c3d4e5f60028", "Currently in Local Service Mode"),
  localSettings: localized("a1b2c3d4e5f6002b", "Local Settings"),
  localSettingsDesc: localized("a1b2c3d4e5f6002c", "Open settings folder or manage model settings separately"),
  settingsFolder: localized("a1b2c3d4e5f6002d", "Settings Folder"),
  modelSettings: localized("a1b2c3d4e5f6002e", "Model Settings"),
};

const directModeEnabled = computed(() => appState.routingMode === "upstream");
const message = useMessage();
const AD_UPDATED_EVENT = "ad:updated";
const OPEN_AD_EVENT = "cursor:open-ad";

const adRuntime = ref(null);
let unsubscribeAdUpdated = null;

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

const homeAds = computed(() => {
  const runtime = adRuntime.value && typeof adRuntime.value === "object" ? adRuntime.value : {};
  const slots = Array.isArray(runtime.slots) && runtime.slots.length > 0 ? runtime.slots : [runtime];
  return slots
    .map((slot, index) => {
      const item = slot && typeof slot === "object" ? slot : {};
      const home = item.home && typeof item.home === "object" ? item.home : {};
      const title = asString(home.title);
      if (
        !title ||
        !asBoolean(item.available) ||
        !asBoolean(item.enabled) ||
        !asString(item.packageHash)
      ) {
        return null;
      }
      return {
        id: asString(item.id) || String(index + 1),
        title,
        subtitle: asString(home.subtitle),
      };
    })
    .filter(Boolean);
});

async function syncAdRuntimeQuietly() {
  try {
    adRuntime.value = await getAdRuntime();
  } catch (_error) {
    adRuntime.value = null;
  }
}

function handleAdUpdated() {
  void syncAdRuntimeQuietly();
}

function handleOpenHomeAd(slotId) {
  window.dispatchEvent(new CustomEvent(OPEN_AD_EVENT, { detail: { slotId: asString(slotId) } }));
}

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || L.serviceError).trim() || L.serviceError,
  });
}

async function handleToggleService() {
  const result = await toggleService();
  if (!result.ok) {
    await showActionError(L.serviceOpFailed, result.error);
  }
}

async function handleRefreshState() {
  const [serviceStateResult] = await Promise.allSettled([
    syncServiceState(),
    syncHomeMetrics(),
  ]);
  if (serviceStateResult.status === "rejected") {
    await showActionError(L.refreshFailed, toUserError(serviceStateResult.reason));
  }
}

async function handleRefreshMetrics() {
  await syncHomeMetrics().catch(() => {});
}

async function handleOpenConfig() {
  try {
    await openConfigWindow();
  } catch (error) {
    await showActionError(L.openFailed, toUserError(error));
  }
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError(L.openFailed, toUserError(error));
  }
}

async function handleDirectModeChange(enabled) {
  const result = await saveRoutingMode(enabled ? "upstream" : "local");
  if (!result.ok) {
    await showActionError(L.switchFailed, result.error);
    return;
  }
  message.success(enabled ? L.switchedDirect : L.switchedLocal);
}

onMounted(() => {
  unsubscribeAdUpdated = Events.On(AD_UPDATED_EVENT, handleAdUpdated);
  void syncAdRuntimeQuietly();
});

onBeforeUnmount(() => {
  if (unsubscribeAdUpdated) {
    unsubscribeAdUpdated();
  }
});
</script>

<template>
  <div class="flex flex-col gap-4 p-4 pt-0 text-[#e5e5e5]">
    <HomeMetricsCard
      :metrics="appState.homeMetrics"
      :loading="appState.homeMetricsLoading"
      :error="appState.homeMetricsError"
      :home-ads="homeAds"
      @refresh="handleRefreshMetrics"
      @open-ad="handleOpenHomeAd"
    />

    <Card>
      <div class="flex flex-col gap-4">
        <div class="flex items-start justify-between gap-4">
          <div class="flex flex-col gap-1">
            <div class="text-sm" :class="appViewState.serviceStatusClass">
              {{ appViewState.serviceStatusText }}
            </div>
          </div>
          <div class="center-row gap-2">
            <Button variant="primary" :disabled="appState.serviceBusy" @click="handleToggleService">
              <span class="icon-[mdi--pause] text-[16px]" v-if="appState.serviceRunning"></span>
              <span class="icon-[mdi--play] text-[16px]" v-else></span>
              <span> {{ appViewState.serviceButtonText }}</span>
            </Button>
          </div>
        </div>

        <div v-if="appState.serviceLastError"
          class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
          {{ appState.serviceLastError }}
        </div>

        <Switch
          :label="L.directMode"
          :description="L.directModeDesc"
          :enabled-text="L.directModeEnabled"
          :disabled-text="L.directModeDisabled"
          :enabled="directModeEnabled"
          :busy="appState.configSaving"
          :disabled="appState.configSaving"
          @change="handleDirectModeChange"
        />
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">{{ L.localSettings }}</h2>
          <div class="text-sm text-[#a3a3a3]">{{ L.localSettingsDesc }}</div>
        </div>
        <div class="center-row gap-2">
          <Button variant="default" @click="handleOpenConfig">{{ L.settingsFolder }}</Button>
          <Button variant="primary" @click="handleOpenModelConfig">{{ L.modelSettings }}</Button>
        </div>
      </div>
    </Card>
  </div>
</template>
