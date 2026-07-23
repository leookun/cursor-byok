<script setup>
import Button from "@/components/ui/Button.vue";
import Switch from "@/components/ui/Switch.vue";
import HomeMetricsCard from "@/components/HomeMetricsCard.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
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
  saveRoutingMode,
  saveOutboundProxy,
  syncHomeMetrics,
  syncServiceState,
  toUserError,
  toggleService,
} from "@/state/appState";
import { Events } from "@wailsio/runtime";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
function openModelConfig() {
  router.push("/model-config");
}

const directModeEnabled = computed(() => appState.routingMode === "upstream");
const message = useMessage();

const PET_LIST_CHANGED_EVENT = "pet:list-changed";


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
  const confirmed = await showModal({
    title: "重置会话统计",
    content: "确定清空首页会话统计（对话轮次 / Token 消耗）？此操作不可撤销。",
    confirmText: "清空",
    cancelText: "取消",
  });
  if (!confirmed) {
    return;
  }
  try {
    await resetHomeMetrics();
    const result = await syncHomeMetrics();
    if (!result?.ok) {
      await showActionError("重置失败", result?.error || "刷新统计失败");
      return;
    }
    message.success("会话统计已清空");
  } catch (error) {
    await showActionError("重置失败", toUserError(error));
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

async function handleSaveOutboundProxy() {
  const result = await saveOutboundProxy({
    httpProxy: appState.outboundProxy.httpProxy,
    httpsProxy: appState.outboundProxy.httpsProxy,
  });
  if (!result.ok) {
    await showActionError("保存失败", result.error);
    return;
  }
  message.success("出站代理已保存");
}

const petEnabled = computed({
  get: () => petSettings.enabled !== false,
  set: async (val) => {
    try {
      // 直接调用 toggle，信任后端返回的真实状态。
      const opened = await togglePetWindow();
      // 根据 toggle 的真实结果同步本地开关。
      petSettings.enabled = opened;
    } catch (err) {
      // 桌宠窗口操作失败：显式提示，避免静默吞错导致开关状态与实际不符。
      await showActionError("桌宠操作失败", toUserError(err));
      // 回退到后端真实状态（避免开关显示与实际不符）。
      try {
        const visible = await isPetWindowVisible();
        petSettings.enabled = visible;
      } catch (_) {
        petSettings.enabled = false;
      }
    }
  },
});

async function handleTogglePet(enabled) {
  petEnabled.value = enabled;
}

// 启动时同步桌宠开关的真实状态，避免 localStorage 与后端不一致
// （后端进程重启后桌宠必然关闭，但 localStorage 可能仍是 enabled=true）。
async function syncPetEnabledState() {
  try {
    const visible = await isPetWindowVisible();
    petSettings.enabled = visible;
  } catch (_) {
    petSettings.enabled = false;
  }
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
  unsubscribePetListChanged = Events.On(PET_LIST_CHANGED_EVENT, (pets) => {
    petList.value = Array.isArray(pets) ? pets : [];
  });
  void syncHomeMetrics();
  void syncPetEnabledState();
  homeMetricsTimer = setInterval(() => {
    void syncHomeMetrics();
  }, 3000);
  refreshPetList();
});

onBeforeUnmount(() => {
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
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pb-8 text-[#e5e5e5]">
    <HomeMetricsCard
      :metrics="appState.homeMetrics"
      :loading="appState.homeMetricsLoading"
      :error="appState.homeMetricsError"
      @reset="handleResetMetrics"
    />

    <div class="flex justify-end">
      <Button variant="secondary" @click="openModelConfig">
        <span class="icon-[mdi--cog-outline] text-[16px]"></span>
        <span class="ml-1">模型配置</span>
      </Button>
    </div>

    <div class="rounded-[8px] bg-[#1e1e1e] p-4 flex flex-col gap-4">
      <!-- Section 1: 服务状态 -->
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

      <!-- Divider 1 -->
      <div class="border-b border-[rgba(255,255,255,0.06)] mx-2"></div>

      <!-- Section 2: 直连模式 -->
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

      <!-- Divider 2 -->
      <div class="border-b border-[rgba(255,255,255,0.06)] mx-2"></div>

      <!-- Section 3: 出站代理 -->
      <div class="flex flex-col gap-3">
        <div class="flex items-center gap-2">
          <span class="icon-[mdi--server-network] text-[16px] text-[#a3a3a3]"></span>
          <h2 class="text-base font-medium text-white">出站代理</h2>
        </div>
        <div class="text-xs text-[#a3a3a3]">配置出站 HTTP/HTTPS 代理，用于连接外部服务</div>
        <div class="flex flex-col gap-3">
          <div class="flex flex-col gap-1.5">
            <label class="text-xs text-[#a3a3a3]">HTTP 代理</label>
            <input
              v-model="appState.outboundProxy.httpProxy"
              type="text"
              placeholder="http://127.0.0.1:7890"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </div>
          <div class="flex flex-col gap-1.5">
            <label class="text-xs text-[#a3a3a3]">HTTPS 代理</label>
            <input
              v-model="appState.outboundProxy.httpsProxy"
              type="text"
              placeholder="http://127.0.0.1:7890"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </div>
        </div>
        <div class="flex justify-end">
          <Button
            variant="primary"
            :disabled="appState.configSaving"
            @click="handleSaveOutboundProxy"
          >
            <span class="icon-[mdi--content-save] text-[14px]"></span>
            <span class="ml-1">保存代理设置</span>
          </Button>
        </div>
      </div>

      <!-- Divider 3 -->
      <div class="border-b border-[rgba(255,255,255,0.06)] mx-2"></div>

      <!-- Section 4: 桌面宠物 -->
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
    </div>
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
