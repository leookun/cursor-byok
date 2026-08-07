<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import CursorAccountCard from "@/components/CursorAccountCard.vue";
import { useMessage } from "@/composables/useMessage";
import { getAdRuntime } from "@/services/clientApi";
import {
  appState,
  appViewState,
  openConfigWindow,
  openModelConfigWindow,
  syncHomeMetrics,
  syncServiceState,
  toUserError,
  toggleService,
} from "@/state/appState";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

const AD_UPDATED_EVENT = "ad:updated";
const OPEN_AD_EVENT = "cursor:open-ad";
const message = useMessage();

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

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

async function handleToggleService() {
  const result = await toggleService();
  if (!result.ok) {
    showActionError("服务操作失败", result.error);
  }
}

async function handleRefreshState() {
  const [serviceStateResult] = await Promise.allSettled([
    syncServiceState(),
    syncHomeMetrics(),
  ]);
  if (serviceStateResult.status === "rejected") {
    showActionError("刷新失败", toUserError(serviceStateResult.reason));
  }
}

async function handleRefreshMetrics() {
  const result = await syncHomeMetrics();
  if (result.ok) {
    message("刷新成功");
    return;
  }
  showActionError("刷新失败", result.error);
}

async function handleOpenConfig() {
  try {
    await openConfigWindow();
  } catch (error) {
    showActionError("打开失败", toUserError(error));
  }
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    showActionError("打开失败", toUserError(error));
  }
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
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto scroll-shadow-bottom p-4 pt-0 text-[#e5e5e5]">
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
        <div class="center-row justify-between gap-4">
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
      </div>
    </Card>

    <CursorAccountCard />

    <Card class="">
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">本地配置</h2>
          <div class="text-sm text-[#a3a3a3]">打开设置目录，或单独管理模型配置</div>
        </div>
        <div class="center-row gap-2">
          <Button variant="default" @click="handleOpenConfig">设置文件夹</Button>
          <Button variant="primary" @click="handleOpenModelConfig">模型配置</Button>
        </div>
      </div>
    </Card>
  </div>
</template>
