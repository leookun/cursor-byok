<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  persistUserConfig,
  QUALITY_TIER_OPTIONS,
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  toUserError,
} from "@/state/appState";
import { onMounted } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const routeModeOptions = ROUTE_MODE_OPTIONS;
const qualityTierOptions = QUALITY_TIER_OPTIONS;

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function handleSaveConfig() {
  const result = await persistUserConfig();
  if (!result.ok) {
    await showActionError("保存失败", result.error);
    return;
  }
  await showModal({
    title: "提示",
    content: "本地配置已保存",
  });
}

async function handleOpenModelConfig() {
  try {
    router.push("/model-config");
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

onMounted(async () => {
  await reloadUserConfig().catch(() => {});
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">本地配置</h2>
          <div class="text-sm text-[#a3a3a3]">
            可配置运行模式和模型渠道；运行日志位于 <code>~/.cursor-local-assistant-v2/logs/</code>
          </div>
        </div>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveConfig">
          {{ appState.configSaving ? "保存中..." : "保存配置" }}
        </Button>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">运行模式</h2>
          <div class="text-sm text-[#a3a3a3]">
            控制白名单主链路请求走本地服务，还是回到原始 Cursor 上游地址
          </div>
        </div>
        <div class="w-[220px] max-w-full">
          <Select
            v-model="appState.routingMode"
            :options="routeModeOptions"
            placeholder="选择模式"
          />
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">界面语言</h2>
          <div class="text-sm text-[#a3a3a3]">
            切换当前界面显示语言，设置会立即生效并保存在本机
          </div>
        </div>
        <LocaleSelect wrapper-class="w-[220px] max-w-full" />
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-4">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">Optimization Runtime</h2>
            <div class="text-sm text-[#a3a3a3]">
              Token 预算分配与成本优化：Quality Tier 影响 MOA 专家选模策略；月度预算用于自动降级
            </div>
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-sm text-[#d4d4d4]">
            <input
              v-model="appState.optimizationEnabled"
              type="checkbox"
              class="h-4 w-4 rounded border-[#404040] bg-[#171717] text-[#3b82f6]"
            />
            启用
          </label>
        </div>
        <div class="flex flex-wrap items-end gap-4">
          <div class="w-[220px] max-w-full">
            <div class="mb-1 text-xs text-[#a3a3a3]">Quality Tier</div>
            <Select
              v-model="appState.optimizationQualityTier"
              :options="qualityTierOptions"
              placeholder="选择质量等级"
              :disabled="!appState.optimizationEnabled"
            />
          </div>
          <div class="w-[160px] max-w-full">
            <div class="mb-1 text-xs text-[#a3a3a3]">月度预算 (USD)</div>
            <input
              v-model.number="appState.optimizationMonthlyBudgetUSD"
              type="number"
              min="1"
              step="1"
              :disabled="!appState.optimizationEnabled"
              class="h-9 w-full rounded-md border border-[#404040] bg-[#171717] px-3 text-sm text-white outline-none focus:border-[#3b82f6] disabled:opacity-50"
            />
          </div>
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">模型配置</h2>
          <div class="text-sm text-[#a3a3a3]">
            已配置 {{ appState.modelAdapters.length }} 个模型适配器
          </div>
        </div>
        <Button variant="primary" @click="handleOpenModelConfig">打开模型配置</Button>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">Virtual Models（虚拟模型）</h2>
          <div class="text-sm text-[#a3a3a3]">
            配置 MOA 等虚拟模型，通过多模型工作流协作提供更高质量的推理
          </div>
        </div>
        <Button variant="primary" @click="router.push('/virtual-models')">打开虚拟模型</Button>
      </div>
    </Card>
  </div>
</template>
