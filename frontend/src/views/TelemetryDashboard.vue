<script setup>
import { ref, onMounted, computed } from "vue";
import Card from "@/components/ui/Card.vue";
import Button from "@/components/ui/Button.vue";
import {
  getAOSLastTraceSummary,
  getAOSExecutionTree,
  replayAOSTrace,
  replayAOSNode,
  getHomeMetricsSummary,
} from "@/services/clientApi";

// Phase 9 切片：Telemetry Dashboard + 交互式 Execution Tree + Replay。
// ponytail: 后端按 sessionID 落盘 trace（telemetry/traces/{sessionID}.json），
// 前端通过 GetAOSExecutionTree 取结构化树；Replay 复用后端已注册的 aos 模型
// 与其生产 ChannelService 重新执行（真实 LLM 调用，非模拟）。

const aosTrace = ref(null);
const aosMeta = ref({});
const homeMetrics = ref(null);
const loading = ref(false);
const error = ref("");

// 交互式执行树
const sessionInput = ref("");
const tree = ref(null); // AOSExecutionTree
const treeError = ref("");
const treeLoading = ref(false);
const collapsed = ref(new Set()); // 折叠的节点 id 集合
const selectedNode = ref(null);
const replayBusy = ref(false);
const replayResult = ref("");
const replayError = ref("");
const nodeReplayBusy = ref({}); // node id → true
const nodeReplayResult = ref({}); // node id → result text
const nodeReplayError = ref({}); // node id → error text

const latestSessionID = computed(() => (tree.value && tree.value.sessionID) || "");

const metricCards = computed(() => {
  const m = aosMeta.value || {};
  const fmt = (k) => (m[k] !== undefined && m[k] !== "" ? m[k] : "—");
  return [
    { label: "总 Token", value: fmt("aos.totalTokens") },
    { label: "Prompt Token", value: fmt("aos.promptTokens") },
    { label: "Completion Token", value: fmt("aos.completionTokens") },
    { label: "任务完成", value: `${fmt("aos.tasksDone")}/${fmt("aos.tasksTotal")}` },
    { label: "耗时 (ms)", value: fmt("aos.durationMS") },
    { label: "Sprint 数", value: fmt("aos.sprints") },
  ];
});

const phaseCards = computed(() => {
  const m = aosMeta.value || {};
  const phases = ["planning", "sprint", "review", "merge"];
  return phases
    .filter((p) => m[`aos.phase.${p}.observed`] === "true")
    .map((p) => ({
      name: p,
      status: m[`aos.phase.${p}.status`] || "?",
      duration: m[`aos.phase.${p}.durationMS`] || "?",
    }));
});

const hasTrace = computed(() => !!(aosTrace.value && aosTrace.value.trim()));
const hasTree = computed(() => !!(tree.value && tree.value.root && tree.value.root.children && tree.value.root.children.length));

function allNodes(node) {
  if (!node) return [];
  const out = [node];
  for (const c of node.children || []) out.push(...allNodes(c));
  return out;
}

function isCollapsed(id) {
  return collapsed.value.has(id);
}

function toggle(id) {
  const next = new Set(collapsed.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  collapsed.value = next;
}

function selectNode(node) {
  selectedNode.value = node;
}

function statusClass(status) {
  if (status === "ok") return "text-green-400";
  if (status === "error") return "text-red-400";
  return "text-[#a3a3a3]";
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const [snap, home] = await Promise.all([
      getAOSLastTraceSummary(),
      getHomeMetricsSummary().catch(() => null),
    ]);
    aosTrace.value = (snap && snap.summary) || "";
    aosMeta.value = (snap && snap.metadata) || {};
    homeMetrics.value = home || null;
  } catch (e) {
    error.value = String(e || "未知错误");
  } finally {
    loading.value = false;
  }
}

async function loadTree() {
  const sid = sessionInput.value.trim();
  if (!sid) {
    treeError.value = "请输入 session ID（执行日志中首行即为 sessionID）。";
    return;
  }
  treeLoading.value = true;
  treeError.value = "";
  selectedNode.value = null;
  replayResult.value = "";
  replayError.value = "";
  nodeReplayBusy.value = {};
  nodeReplayResult.value = {};
  nodeReplayError.value = {};
  try {
    const t = await getAOSExecutionTree(sid);
    if (!t || !t.root || !t.root.children || t.root.children.length === 0) {
      treeError.value = "未找到该 session 的执行树（可能尚未落盘或 sessionID 有误）。";
      tree.value = null;
    } else {
      tree.value = t;
    }
  } catch (e) {
    treeError.value = String(e || "加载执行树失败");
    tree.value = null;
  } finally {
    treeLoading.value = false;
  }
}

async function onReplay() {
  const sid = (sessionInput.value.trim()) || latestSessionID.value;
  if (!sid) {
    replayError.value = "请先输入或加载一个有效的 session ID。";
    return;
  }
  replayBusy.value = true;
  replayError.value = "";
  replayResult.value = "";
  try {
    const text = await replayAOSTrace(sid);
    replayResult.value = text || "（空结果）";
  } catch (e) {
    replayError.value = String(e || "Replay 失败");
  } finally {
    replayBusy.value = false;
  }
}

// Parse node index from node id like "aos-1234567890-3" → 3
function nodeIndexFromID(nodeID, sessionID) {
  const prefix = sessionID + "-";
  if (!nodeID || !nodeID.startsWith(prefix)) return -1;
  const idx = parseInt(nodeID.slice(prefix.length), 10);
  return Number.isNaN(idx) ? -1 : idx;
}

async function onReplayNode(node) {
  const sid = latestSessionID.value || sessionInput.value.trim();
  if (!sid) {
    nodeReplayError.value = { ...nodeReplayError.value, [node.id]: "缺少 session ID" };
    return;
  }
  const idx = nodeIndexFromID(node.id, sid);
  if (idx < 0) {
    nodeReplayError.value = { ...nodeReplayError.value, [node.id]: "无法解析节点索引" };
    return;
  }
  nodeReplayBusy.value = { ...nodeReplayBusy.value, [node.id]: true };
  nodeReplayError.value = { ...nodeReplayError.value, [node.id]: "" };
  nodeReplayResult.value = { ...nodeReplayResult.value, [node.id]: "" };
  try {
    const text = await replayAOSNode(sid, idx);
    nodeReplayResult.value = { ...nodeReplayResult.value, [node.id]: text || "（空结果）" };
  } catch (e) {
    nodeReplayError.value = { ...nodeReplayError.value, [node.id]: String(e || "节点 Replay 失败") };
  } finally {
    nodeReplayBusy.value = { ...nodeReplayBusy.value, [node.id]: false };
  }
}

onMounted(load);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">Telemetry Dashboard</h2>
          <div class="text-sm text-[#a3a3a3]">
            AOS 执行追踪、Token / 成本 / 延迟指标与交互式执行树
          </div>
        </div>
        <Button variant="primary" :disabled="loading" @click="load">
          {{ loading ? "加载中..." : "刷新" }}
        </Button>
      </div>
    </Card>

    <div v-if="error" class="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
      {{ error }}
    </div>

    <!-- Home metrics（通用遥测） -->
    <Card v-if="homeMetrics">
      <div class="text-sm font-medium text-white mb-3">全局指标</div>
      <div class="grid grid-cols-2 gap-3 text-sm md:grid-cols-4">
        <div class="rounded bg-[#1f1f1f] p-2">
          <div class="text-[#a3a3a3]">总对话轮次</div>
          <div class="text-lg font-medium text-white">{{ homeMetrics.turnsTotal ?? 0 }}</div>
        </div>
        <div class="rounded bg-[#1f1f1f] p-2">
          <div class="text-[#a3a3a3]">请求 Token</div>
          <div class="text-lg font-medium text-white">{{ homeMetrics.requestTokensTotal ?? 0 }}</div>
        </div>
        <div class="rounded bg-[#1f1f1f] p-2">
          <div class="text-[#a3a3a3]">缓存读 Token</div>
          <div class="text-lg font-medium text-white">{{ homeMetrics.cacheReadTokens ?? 0 }}</div>
        </div>
        <div class="rounded bg-[#1f1f1f] p-2">
          <div class="text-[#a3a3a3]">命中率</div>
          <div class="text-lg font-medium text-white">
            {{ homeMetrics.cacheHitRate != null ? (homeMetrics.cacheHitRate * 100).toFixed(1) + "%" : "—" }}
          </div>
        </div>
      </div>
    </Card>

    <!-- AOS trace metrics -->
    <Card v-if="hasTrace">
      <div class="text-sm font-medium text-white mb-3">AOS 执行指标</div>
      <div class="grid grid-cols-2 gap-3 text-sm md:grid-cols-3">
        <div
          v-for="m in metricCards"
          :key="m.label"
          class="rounded bg-[#1f1f1f] p-2"
        >
          <div class="text-[#a3a3a3]">{{ m.label }}</div>
          <div class="text-lg font-medium text-white">{{ m.value }}</div>
        </div>
      </div>
    </Card>

    <!-- Phase breakdown -->
    <Card v-if="phaseCards.length">
      <div class="text-sm font-medium text-white mb-3">阶段分解</div>
      <div class="flex flex-col gap-2">
        <div
          v-for="p in phaseCards"
          :key="p.name"
          class="flex items-center justify-between rounded bg-[#1f1f1f] px-3 py-2"
        >
          <span class="text-sm font-medium text-white">{{ p.name }}</span>
          <div class="flex items-center gap-3 text-xs">
            <span class="text-[#a3a3a3]">{{ p.duration }}ms</span>
            <span
              :class="p.status === 'ok' ? 'text-green-400' : p.status === 'error' ? 'text-red-400' : 'text-[#a3a3a3]'"
            >{{ p.status }}</span>
          </div>
        </div>
      </div>
    </Card>

    <!-- 交互式 Execution Tree -->
    <Card>
      <div class="flex items-center justify-between gap-4 mb-3">
        <div class="text-sm font-medium text-white">Execution Tree（交互式）</div>
        <div class="flex items-center gap-2">
          <input
            v-model="sessionInput"
            type="text"
            placeholder="session ID（留空则提示）"
            class="w-56 rounded bg-[#1f1f1f] px-2 py-1 text-xs text-white outline-none ring-1 ring-[#3a3a3a] focus:ring-[#10AD5D]"
          />
          <Button variant="default" :disabled="treeLoading" @click="loadTree">
            {{ treeLoading ? "加载中..." : "加载" }}
          </Button>
          <Button
            variant="primary"
            :disabled="replayBusy"
            :title="latestSessionID || sessionInput"
            @click="onReplay"
          >
            {{ replayBusy ? "Replay 中..." : "Replay" }}
          </Button>
        </div>
      </div>

      <div v-if="treeError" class="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        {{ treeError }}
      </div>

      <div v-else-if="hasTree" class="flex gap-4">
        <!-- 折叠树 -->
        <div class="w-1/2 overflow-auto rounded bg-[#1a1a1a] p-2 font-mono text-xs">
          <div class="leading-relaxed">
            <span
              class="cursor-pointer"
              @click="toggle(tree.root.id)"
            >{{ isCollapsed(tree.root.id) ? "▸" : "▾" }}</span>
            <span class="text-white">{{ tree.root.role }} / session</span>
            <span v-if="tree.root.id" class="text-[#666]"> ({{ tree.root.id }})</span>
          </div>
          <template v-if="!isCollapsed(tree.root.id)">
            <div
              v-for="node in tree.root.children"
              :key="node.id"
              class="mt-1 leading-relaxed"
            >
              <div
                class="cursor-pointer rounded px-1 flex items-center justify-between gap-1"
                :class="selectedNode && selectedNode.id === node.id ? 'bg-[#2a3a2a]' : 'hover:bg-[#222]'"
                @click="selectNode(node)"
              >
                <div class="flex items-center gap-1 min-w-0">
                  <span :class="statusClass(node.status)">●</span>
                  <span class="text-white">{{ node.role }} / {{ node.model || "—" }}</span>
                  <span class="text-[#a3a3a3]"> {{ node.status }} {{ node.duration }}ms</span>
                  <span v-if="node.execID" class="text-[#666] hidden sm:inline"> exec={{ node.execID }}</span>
                </div>
                <button
                  v-if="node.model && node.prompt"
                  class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium text-[#10AD5D] hover:bg-[#10AD5D]/15 transition-colors"
                  :disabled="nodeReplayBusy[node.id]"
                  @click.stop="onReplayNode(node)"
                >
                  {{ nodeReplayBusy[node.id] ? "重放中..." : "重放此节点" }}
                </button>
              </div>
              <!-- Node replay result inline -->
              <div v-if="nodeReplayResult[node.id] || nodeReplayError[node.id]" class="mt-1 ml-4 rounded bg-[#0f2a1a] p-2 text-xs">
                <div v-if="nodeReplayError[node.id]" class="text-red-400">{{ nodeReplayError[node.id] }}</div>
                <pre v-else class="max-h-24 overflow-auto whitespace-pre-wrap text-[#9fdf9f]">{{ nodeReplayResult[node.id] }}</pre>
              </div>
            </div>
          </template>
        </div>

        <!-- 节点详情 -->
        <div class="w-1/2 overflow-auto rounded bg-[#1a1a1a] p-3 text-xs">
          <template v-if="selectedNode">
            <div class="mb-2 flex items-center gap-2">
              <span :class="statusClass(selectedNode.status)">●</span>
              <span class="font-medium text-white">{{ selectedNode.role }}</span>
              <span class="text-[#a3a3a3]">/ {{ selectedNode.model || "—" }}</span>
              <span class="text-[#a3a3a3]">({{ selectedNode.status }}, {{ selectedNode.duration }}ms)</span>
            </div>
            <div v-if="selectedNode.execID" class="mb-2 text-[#a3a3a3]">execID: {{ selectedNode.execID }}</div>
            <div class="mb-1 text-[#a3a3a3]">Prompt</div>
            <pre class="mb-3 max-h-40 overflow-auto whitespace-pre-wrap rounded bg-[#121212] p-2 text-[#ccc]">{{ selectedNode.prompt || "（无）" }}</pre>
            <div class="mb-1 text-[#a3a3a3]">Response</div>
            <pre class="max-h-56 overflow-auto whitespace-pre-wrap rounded bg-[#121212] p-2 text-[#ccc]">{{ selectedNode.response || "（无）" }}</pre>
            <template v-if="selectedNode.model && selectedNode.prompt">
              <div v-if="nodeReplayBusy[selectedNode.id]" class="mt-2 text-[#10AD5D] text-xs">重放中...</div>
              <div v-else-if="nodeReplayError[selectedNode.id]" class="mt-2 rounded bg-red-500/10 p-2 text-xs text-red-400">{{ nodeReplayError[selectedNode.id] }}</div>
              <div v-else-if="nodeReplayResult[selectedNode.id]" class="mt-2">
                <div class="mb-1 text-[#a3a3a3] text-xs">🔄 重放结果</div>
                <pre class="max-h-40 overflow-auto whitespace-pre-wrap rounded bg-[#0f2a1a] p-2 text-[#9fdf9f]">{{ nodeReplayResult[selectedNode.id] }}</pre>
              </div>
            </template>
          </template>
          <div v-else class="text-[#a3a3a3]">点击左侧任意节点查看 Prompt / Response 详情。</div>
        </div>
      </div>

      <div v-else-if="!treeLoading && !treeError" class="py-4 text-center text-sm text-[#a3a3a3]">
        输入 session ID 并点击「加载」查看交互式执行树（AOS 执行后会自动落盘到 telemetry/traces/）。
      </div>
    </Card>

    <!-- Replay 结果 -->
    <Card v-if="replayResult || replayError">
      <div class="text-sm font-medium text-white mb-3">Replay 结果</div>
      <div v-if="replayError" class="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        {{ replayError }}
      </div>
      <pre v-else class="max-h-72 overflow-auto whitespace-pre-wrap rounded bg-[#1a1a1a] p-3 text-xs text-[#ccc]">{{ replayResult }}</pre>
    </Card>

    <Card v-if="!loading && !hasTrace && !error">
      <div class="py-4 text-center text-sm text-[#a3a3a3]">
        暂无 AOS 执行记录。在 Cursor 中选用 AOS 模型跑一轮后点刷新。
      </div>
    </Card>
  </div>
</template>