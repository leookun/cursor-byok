<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  persistUserConfig,
  reloadUserConfig,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();

// MOA 角色列表
const MOA_ROLES = [
  { id: "planner", label: "Planner（规划器）", desc: "分析用户请求，决定需要哪些专家" },
  { id: "coding", label: "Coding Expert（编码专家）", desc: "代码生成、调试、审查" },
  { id: "research", label: "Research Expert（研究专家）", desc: "信息收集与分析" },
  { id: "reasoning", label: "Reasoning Expert（推理专家）", desc: "逻辑推理与数学分析" },
  { id: "critic", label: "Critic（批评者）", desc: "发现漏洞、遗漏、逻辑错误" },
  { id: "judge", label: "Judge（评判者）", desc: "评分、一致性检查" },
  { id: "aggregator", label: "Aggregator（聚合器）", desc: "融合多个专家输出为最终答案" },
];

// 所有可用 adapter 的下拉选项
const adapterOptions = computed(() => {
  const options = [{ value: "", label: "使用默认（第一个）" }];
  for (const adapter of appState.modelAdapters) {
    options.push({
      value: adapter.id || adapter.displayName,
      label: `${adapter.displayName} (${adapter.modelID})`,
    });
  }
  return options;
});

// MOA 是否启用
const moaEnabled = ref(false);
// 各角色的 adapter 绑定
const roleBindings = ref({});

// 初始化
onMounted(async () => {
  await reloadUserConfig().catch(() => {});
  // 从 appState 读取虚拟模型配置
  const vmConfig = appState.virtualModels || {};
  const moa = vmConfig.moa || {};
  moaEnabled.value = !!moa.enabled;
  const nodes = moa.nodes || {};
  const bindings = {};
  for (const role of MOA_ROLES) {
    bindings[role.id] = (nodes[role.id] && nodes[role.id].adapterID) || "";
  }
  roleBindings.value = bindings;
});

async function handleSave() {
  try {
    // 构建虚拟模型配置
    const nodes = {};
    for (const role of MOA_ROLES) {
      const adapterID = roleBindings.value[role.id] || "";
      nodes[role.id] = { adapterID, enabled: true };
    }
    const virtualModels = {
      moa: {
        enabled: moaEnabled.value,
        workflowID: "moa-default",
        planner: { adapterID: roleBindings.value.planner || "", enabled: true },
        nodes,
      },
    };
    appState.virtualModels = virtualModels;
    const result = await persistUserConfig();
    if (!result.ok) {
      await showModal({ title: "保存失败", content: String(result.error || "服务错误").trim() || "服务错误" });
      return;
    }
    await showModal({ title: "提示", content: "虚拟模型配置已保存" });
  } catch (error) {
    await showModal({ title: "保存失败", content: toUserError(error) });
  }
}

function handleBack() {
  router.push("/config");
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <!-- 标题栏 -->
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">Virtual Models（虚拟模型）</h2>
          <div class="text-sm text-[#a3a3a3]">
            虚拟模型通过编排多个物理模型的工作流来提供更高质量的推理结果
          </div>
        </div>
        <div class="flex gap-2">
          <Button variant="secondary" @click="handleBack">返回</Button>
          <Button variant="primary" :disabled="appState.configSaving" @click="handleSave">
            {{ appState.configSaving ? "保存中..." : "保存配置" }}
          </Button>
        </div>
      </div>
    </Card>

    <!-- MOA 配置 -->
    <Card>
      <div class="space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm font-medium text-white">MOA（Multi-model Orchestration Architecture）</h3>
            <div class="text-xs text-[#a3a3a3] mt-1">
              多模型协作架构 — Planner → 多专家并行 → Critic → Judge → Aggregator
            </div>
          </div>
          <label class="relative inline-flex cursor-pointer items-center">
            <input
              type="checkbox"
              v-model="moaEnabled"
              class="peer sr-only"
            />
            <div class="h-6 w-11 rounded-full bg-[#404040] peer-checked:bg-[#007acc] peer-focus:outline-none after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-all peer-checked:after:translate-x-full"></div>
          </label>
        </div>

        <!-- 启用后才显示角色配置 -->
        <div v-if="moaEnabled" class="space-y-3 border-t border-[#333] pt-4">
          <div class="text-xs text-[#a3a3a3]">
            为每个角色选择使用哪个已配置的 Model Adapter。留空表示使用默认（第一个 adapter）。
          </div>
          <div
            v-for="role in MOA_ROLES"
            :key="role.id"
            class="flex items-center gap-4 py-2"
          >
            <div class="w-[200px] shrink-0">
              <div class="text-sm text-white">{{ role.label }}</div>
              <div class="text-xs text-[#666]">{{ role.desc }}</div>
            </div>
            <div class="flex-1 max-w-[300px]">
              <Select
                v-model="roleBindings[role.id]"
                :options="adapterOptions"
                :placeholder="`选择 ${role.label} 的模型`"
              />
            </div>
          </div>
        </div>
      </div>
    </Card>

    <!-- 更多虚拟模型预留 -->
    <Card v-if="moaEnabled">
      <div class="text-sm text-[#a3a3a3]">
        <span class="text-[#007acc]">提示：</span>
        更多虚拟模型（Reflection、Best-of-N、Tree-of-Thought、Debate）将在后续版本中支持。
        MOA 在 Cursor 模型选择器中显示为 <code class="text-[#e5e5e5] bg-[#2a2a2a] px-1 rounded">MOA</code>。
      </div>
    </Card>
  </div>
</template>
