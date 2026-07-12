<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Switch from "@/components/ui/Switch.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  getAdRuntime,
  resetHomeMetrics,
  togglePetWindow,
  isPetWindowVisible,
  openPetsDirectory,
  scanPets,
  switchPet,
} from "@/services/clientApi";
import { petSettings } from "@/pet/petSettings";
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
import { useRouter } from "vue-router";
import { asString, asBoolean } from "@/utils/typeCast";

const router = useRouter();
const directModeEnabled = computed(() => appState.routingMode === "upstream");
const message = useMessage();

const AD_UPDATED_EVENT = "ad:updated";
const PET_LIST_CHANGED_EVENT = "pet:list-changed";
const OPEN_AD_EVENT = "cursor:open-ad";

const adRuntime = ref(null);
let unsubscribeAdUpdated = null;

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
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function handleToggleService() {
  const result = await toggleService();
  if (!result.ok) {
    await showActionError("服务操作失败", result.error);
  }
}

async function handleRefreshState() {
  const [serviceStateResult] = await Promise.allSettled([
    syncServiceState(),
    syncHomeMetrics(),
  ]);
  if (serviceStateResult.status === "rejected") {
    await showActionError("刷新失败", toUserError(serviceStateResult.reason));
  }
}

async function handleResetMetrics() {
  await resetHomeMetrics();
  await syncHomeMetrics();
}

async function handleOpenConfig() {
  try {
    await openConfigWindow();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

async function handleOpenModelConfig() {
  try {
    router.push("/model-config");
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

async function handleDirectModeChange(enabled) {
  const result = await saveRoutingMode(enabled ? "upstream" : "local");
  if (!result.ok) {
    await showActionError("切换失败", result.error);
    return;
  }
  message.success(enabled ? "已切换到直连 Cursor 模式" : "已切换到本地服务模式");
}

const petEnabled = computed({
  get: () => petSettings.enabled !== false,
  set: async (val) => {
    try {
      // 直接调用 toggle，信任后端返回的真实状态。
      // 不再先查 isPetWindowVisible（避免异步竞态导致连点错乱）。
      const opened = await togglePetWindow();
      // 根据 toggle 的真实结果同步本地开关。
      // 若 toggle 返回的状态与 val 不一致，以后端为准（用户可能快速连点）。
      petSettings.enabled = opened;
    } catch (_) {
      // 桌宠窗口操作失败静默忽略
    }
  },
});

async function handleTogglePet(enabled) {
  petEnabled.value = enabled;
}

function handleOpenPetsFolder() {
  openPetsDirectory();
}

// 选择宠物：调用后端 SwitchPet 重启桌宠引擎
async function handleSelectPet(petId) {
  if (!petId) return;
  petSettings.activePetId = petId;
  try {
    await switchPet(petId);
  } catch (error) {
    await showActionError("切换桌宠失败", toUserError(error));
  }
}

// 宠物列表：初始加载 + 事件推送
const petList = ref([]);
const petLoadError = ref("");
let unsubscribePetListChanged = null;

async function refreshPetList() {
  petLoadError.value = "";
  try {
    const pets = await scanPets();
    petList.value = Array.isArray(pets) ? pets : [];
  } catch (error) {
    petLoadError.value = toUserError(error);
    // 同时弹窗提示，便于第一时间看到 RPC 调用失败原因
    await showActionError("加载桌宠列表失败", toUserError(error));
  }
}

let homeMetricsTimer = null;

onMounted(() => {
  unsubscribeAdUpdated = Events.On(AD_UPDATED_EVENT, handleAdUpdated);
  unsubscribePetListChanged = Events.On(PET_LIST_CHANGED_EVENT, (pets) => {
    petList.value = Array.isArray(pets) ? pets : [];
  });
  void syncAdRuntimeQuietly();
  void syncHomeMetrics();
  homeMetricsTimer = setInterval(() => {
    void syncHomeMetrics();
  }, 3000);
  refreshPetList();
});

onBeforeUnmount(() => {
  if (unsubscribeAdUpdated) {
    unsubscribeAdUpdated();
    unsubscribeAdUpdated = null;
  }
  if (unsubscribePetListChanged) {
    unsubscribePetListChanged();
    unsubscribePetListChanged = null;
  }
  if (homeMetricsTimer) {
    clearInterval(homeMetricsTimer);
    homeMetricsTimer = null;
  }
});

// 状态标签显示
function statusLabel(pet) {
  switch (pet.status) {
    case "ready":
      return "就绪";
    case "warning":
      return "警告";
    case "broken":
      return "损坏";
    case "running":
      return "运行中";
    default:
      return pet.status || "未知";
  }
}

function statusColor(pet) {
  switch (pet.status) {
    case "ready":
      return "text-green-400";
    case "warning":
      return "text-yellow-400";
    case "broken":
      return "text-red-400";
    default:
      return "text-[#a3a3a3]";
  }
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <HomeMetricsCard
      :metrics="appState.homeMetrics"
      :loading="appState.homeMetricsLoading"
      :error="appState.homeMetricsError"
      :home-ads="homeAds"
      @reset="handleResetMetrics"
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
          label="直连模式"
          description="开启后，Cursor将直接接通官方，请勿开启"
          enabled-text="当前为直连模式"
          disabled-text="当前为本地服务模式"
          :enabled="directModeEnabled"
          :busy="appState.configSaving"
          :disabled="appState.configSaving"
          @change="handleDirectModeChange"
        />
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-3">
        <!-- 桌面宠物：标题+开关 -->
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">桌面宠物</h2>
            <div class="text-sm text-[#a3a3a3]">在桌面上显示可爱的动画角色</div>
          </div>
          <Switch
            :enabled="petSettings.enabled !== false"
            :busy="false"
            @change="handleTogglePet"
          />
        </div>

        <!-- 宠物列表：每个宠物一个按钮 -->
        <div class="flex flex-col gap-2">
          <div class="flex items-center justify-between">
            <span class="text-xs text-[#a3a3a3]">点击切换桌宠（{{ petList.length }} 个可用）</span>
            <Button variant="secondary" @click="handleOpenPetsFolder">
              📂 打开文件夹
            </Button>
          </div>
          <div v-if="petLoadError" class="text-xs text-[#ff6b6b] py-1">
            ⚠️ 加载失败：{{ petLoadError }}
          </div>
          <div v-if="petList.length === 0" class="text-xs text-[#6f6f6f] py-2">
            暂无可用宠物 — 复制宠物文件夹到 pets 目录即可自动发现
          </div>
          <div v-else class="flex flex-col gap-1.5">
            <button
              v-for="pet in petList"
              :key="pet.id"
              class="pet-select-btn"
              :class="{ active: petSettings.activePetId === pet.id }"
              @click="handleSelectPet(pet.id)"
            >
              <span class="pet-icon">🐾</span>
              <div class="flex-1 flex flex-col items-start min-w-0">
                <span class="pet-label">{{ pet.name }}</span>
                <span class="text-[10px] text-[#7a7a7a] truncate w-full">
                  {{ pet.id }}{{ pet.version ? " · v" + pet.version : "" }}
                </span>
              </div>
              <span class="text-[10px] shrink-0" :class="statusColor(pet)">
                {{ statusLabel(pet) }}
              </span>
            </button>
          </div>
        </div>

        <!-- 本地配置 -->
        <div class="pt-3 mt-1 border-t border-[#2a2a2a] flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">本地配置</h2>
            <div class="text-sm text-[#a3a3a3]">打开设置目录，或单独管理模型配置</div>
          </div>
          <div class="center-row gap-2 shrink-0">
            <Button variant="default" @click="handleOpenConfig">设置文件夹</Button>
            <Button variant="primary" @click="handleOpenModelConfig">模型配置</Button>
          </div>
        </div>
      </div>
    </Card>
  </div>
</template>

<style scoped>
.pet-select-btn {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
  color: #ccc;
  cursor: pointer;
  transition: all 0.15s;
  text-align: left;
}
.pet-select-btn:hover {
  background: rgba(255, 255, 255, 0.08);
  border-color: rgba(255, 255, 255, 0.15);
}
.pet-select-btn.active {
  border-color: #7c5cfc;
  background: rgba(124, 92, 252, 0.15);
  color: #fff;
}
.pet-icon {
  font-size: 18px;
}
.pet-label {
  font-size: 13px;
  font-weight: 500;
}
</style>
