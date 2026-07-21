<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import Input from "@/components/ui/Input.vue";

import { showModal } from "@/composables/useModal";
import {
  appState,
  persistUserConfig,
  reloadUserConfig,
  toUserError,
} from "@/state/appState";
import { getAOSLastTraceSummary } from "@/services/clientApi";
import { computed, onMounted, ref, reactive } from "vue";
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

// AOS 是否启用
const aosEnabled = ref(false);
const aosExecutionMode = ref("cursor_task");
// AOS Leader adapter
const aosLeaderAdapter = ref("");
// AOS Members
const aosMembers = ref([]);
// Phase 26f: last AOS execution summary (read-only)
const aosLastTraceSummary = ref("");
const aosLastTraceMeta = ref({});
const aosTraceLoading = ref(false);

async function refreshAosTrace() {
  aosTraceLoading.value = true;
  try {
    const snap = await getAOSLastTraceSummary();
    aosLastTraceSummary.value = (snap && snap.summary) || "";
    aosLastTraceMeta.value = (snap && snap.metadata) || {};
  } catch (e) {
    aosLastTraceSummary.value = "";
    aosLastTraceMeta.value = {};
  } finally {
    aosTraceLoading.value = false;
  }
}

function addAosMember() {
  aosMembers.value.push({ id: "", name: "", adapterID: "", systemPrompt: "" });
}

function removeAosMember(index) {
  aosMembers.value.splice(index, 1);
}

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
  // AOS config
  const aos = vmConfig.aos || {};
  aosEnabled.value = !!aos.enabled;
  aosExecutionMode.value = String(aos.executionMode || "").trim().toLowerCase() === "internal" ? "internal" : "cursor_task";
  aosLeaderAdapter.value = (aos.leader && aos.leader.adapterID) || "";
  aosMembers.value = (aos.members || []).map((m) => ({ id: m.id || "", name: m.name || "", adapterID: m.adapterID || "", systemPrompt: m.systemPrompt || "" }));
  await refreshAosTrace();
});

async function handleSave() {
  try {
    // Build AOS config
    const aosMembers = [];
    for (const m of aosMembers.value) {
      if (m.id && m.adapterID) {
        aosMembers.push({ id: m.id, name: m.name || m.id, adapterID: m.adapterID, systemPrompt: m.systemPrompt || "" });
      }
    }
    const aos = {
      enabled: aosEnabled.value,
      executionMode: aosExecutionMode.value === "internal" ? "internal" : "cursor_task",
      leader: { adapterID: aosLeaderAdapter.value },
      members: aosMembers,
    };
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
      aos,
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

// Phase 7 切片：只读工作流拓扑（SVG 节点坐标，x 间距 140）。
const nodeFill = "#1f1f1f";
const moaFlow = computed(() =>
  ["planner", "experts", "critic", "judge", "aggregator"].map((id, i) => ({
    id,
    x: 10 + i * 140,
    label: { planner: "Planner", experts: "Experts", critic: "Critic", judge: "Judge", aggregator: "Aggregator" }[id],
    fill: nodeFill,
  })),
);
const aosFlow = computed(() =>
  ["leader", "sprint", "review", "merge"].map((id, i) => ({
    id,
    x: 10 + i * 140,
    label: { leader: "Leader", sprint: "Sprint", review: "Review", merge: "Merge" }[id],
    fill: nodeFill,
  })),
);

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

    <!-- AOS 配置 -->
    <Card>
      <div class="space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm font-medium text-white">AOS（AI Organization System）</h3>
            <div class="text-xs text-[#a3a3a3] mt-1">
              AI 组织系统 — Leader（架构师+Tech Lead）协调 Members 通过 Workspace 协作开发
            </div>
          </div>
          <label class="relative inline-flex cursor-pointer items-center">
            <input
              type="checkbox"
              v-model="aosEnabled"
              class="peer sr-only"
            />
            <div class="h-6 w-11 rounded-full bg-[#404040] peer-checked:bg-[#007acc] peer-focus:outline-none after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-all peer-checked:after:translate-x-full"></div>
          </label>
        </div>

        <div v-if="aosEnabled" class="space-y-4 border-t border-[#333] pt-4">
          <!-- Leader -->
          <div class="flex items-center gap-4">
            <div class="w-[200px] shrink-0">
              <div class="text-sm text-white">Leader</div>
              <div class="text-xs text-[#666]">最聪明最全能的模型</div>
            </div>
            <div class="flex-1 max-w-[300px]">
              <Select
                v-model="aosLeaderAdapter"
                :options="adapterOptions"
                placeholder="选择 Leader 模型"
              />
            </div>
          </div>

          <!-- Members -->
          <div class="space-y-3">
            <div class="flex items-center justify-between">
              <div class="text-xs text-[#a3a3a3]">Members = Prompt + ModelAdapter，用户可自定义任意角色</div>
              <Button variant="secondary" size="sm" @click="addAosMember">+ 添加成员</Button>
            </div>
            <div
              v-for="(member, index) in aosMembers"
              :key="index"
              class="space-y-2 rounded-lg border border-[#333] p-3"
            >
              <div class="flex items-center gap-2">
                <input
                  v-model="member.id"
                  class="flex-1 rounded bg-[#2a2a2a] px-2 py-1 text-sm text-white outline-none"
                  placeholder="ID (如 frontend)"
                />
                <input
                  v-model="member.name"
                  class="flex-1 rounded bg-[#2a2a2a] px-2 py-1 text-sm text-white outline-none"
                  placeholder="名称 (如 Frontend Engineer)"
                />
                <Select
                  v-model="member.adapterID"
                  :options="adapterOptions"
                  placeholder="选择模型"
                  class="flex-1"
                />
                <Button variant="secondary" size="sm" @click="removeAosMember(index)">删除</Button>
              </div>
              <textarea
                v-model="member.systemPrompt"
                class="w-full rounded bg-[#2a2a2a] px-2 py-1 text-sm text-white outline-none"
                rows="2"
                placeholder="角色提示词 (如: 你是一位资深 React 开发者...)"
              />
            </div>
          </div>

          <!-- Phase 26f: last AOS execution summary -->
          <div class="space-y-2 rounded-lg border border-[#333] p-3">
            <div class="flex items-center justify-between">
              <div>
                <div class="text-sm text-white">最近 AOS 执行摘要</div>
                <div class="text-xs text-[#666]">进程内最近一次 AOS turn 的 ExecutionTrace（重启后清空）</div>
              </div>
              <Button variant="secondary" size="sm" :disabled="aosTraceLoading" @click="refreshAosTrace">
                {{ aosTraceLoading ? "加载中…" : "刷新" }}
              </Button>
            </div>
            <pre
              v-if="aosLastTraceSummary"
              class="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-[#1a1a1a] p-2 text-xs text-[#ccc]"
            >{{ aosLastTraceSummary }}</pre>
            <div v-else class="text-xs text-[#666]">暂无执行记录。在 Cursor 中选用 AOS 模型跑一轮后点刷新。</div>
            <div v-if="aosLastTraceMeta && aosLastTraceMeta['aos.tasksTotal']" class="text-xs text-[#888]">
              tasks {{ aosLastTraceMeta["aos.tasksDone"] || 0 }}/{{ aosLastTraceMeta["aos.tasksTotal"] }}
              · tokens {{ aosLastTraceMeta["aos.totalTokens"] || 0 }}
              · {{ aosLastTraceMeta["aos.durationMS"] || "?" }}ms
            </div>
          </div>
        </div>
      </div>
    </Card>

    <!-- Phase 7 切片：可视化工作流（只读 SVG 图） -->
    <Card>
      <div class="text-sm font-medium text-white mb-1">工作流可视化</div>
      <div class="text-xs text-[#a3a3a3] mb-3">
        MOA 编排拓扑（只读）。完整拖拽编辑器为后续工作。
      </div>

      <!-- MOA 流程 -->
      <div v-if="moaEnabled" class="space-y-2">
        <div class="text-xs text-[#888]">MOA</div>
        <svg viewBox="0 0 720 140" class="w-full max-w-2xl" role="img" aria-label="MOA workflow">
          <defs>
            <marker id="arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
              <path d="M0,0 L6,3 L0,6 Z" fill="#555" />
            </marker>
          </defs>
          <g font-size="11" fill="#e5e5e5" text-anchor="middle">
            <g v-for="(n, i) in moaFlow" :key="n.id">
              <rect :x="n.x" y="50" width="120" height="38" rx="6"
                :fill="n.fill" stroke="#333" />
              <text :x="n.x + 60" y="73">{{ n.label }}</text>
              <line v-if="i < moaFlow.length - 1" :x1="n.x + 120" y1="69"
                :x2="moaFlow[i + 1].x" y2="69" stroke="#555" stroke-width="1.5"
                marker-end="url(#arrow)" />
            </g>
          </g>
        </svg>
      </div>

      <!-- AOS 流程 -->
      <div v-if="aosEnabled" class="mt-4 space-y-2">
        <div class="text-xs text-[#888]">AOS</div>
        <svg viewBox="0 0 720 140" class="w-full max-w-2xl" role="img" aria-label="AOS workflow">
          <g font-size="11" fill="#e5e5e5" text-anchor="middle">
            <g v-for="(n, i) in aosFlow" :key="n.id">
              <rect :x="n.x" y="50" width="120" height="38" rx="6"
                :fill="n.fill" stroke="#333" />
              <text :x="n.x + 60" y="73">{{ n.label }}</text>
              <line v-if="i < aosFlow.length - 1" :x1="n.x + 120" y1="69"
                :x2="aosFlow[i + 1].x" y2="69" stroke="#555" stroke-width="1.5"
                marker-end="url(#arrow)" />
            </g>
          </g>
        </svg>
        <div v-if="aosMembers.length" class="text-xs text-[#888] mt-2">
          Members（{{ aosMembers.length }}）：
          <span v-for="(m, i) in aosMembers" :key="i" class="mr-2 text-[#ccc]">
            {{ m.name || m.id || "?" }}
          </span>
        </div>
      </div>

      <div
        v-if="!moaEnabled && !aosEnabled"
        class="text-xs text-[#666]"
      >启用 MOA 或 AOS 后显示对应工作流拓扑。</div>
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
