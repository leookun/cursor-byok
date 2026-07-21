<script setup>
import { ref, reactive, computed, onMounted, onBeforeUnmount } from "vue";
import { useMessage } from "@/composables/useMessage";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import {
  listWorkflows,
  getWorkflow,
  createWorkflow,
  updateWorkflow,
  deleteWorkflow,
  executeWorkflow,
} from "@/services/workflowApi";

const message = useMessage();

const NODE_TYPES = ["start", "task", "condition", "end"];
const NODE_COLORS = {
  start: "#10AD5D",
  task: "#3b82f6",
  condition: "#f59e0b",
  end: "#ef4444",
};

const nodeSize = 64;
let idSeq = 1;
function newId(prefix) {
  return `${prefix}_${Date.now().toString(36)}_${idSeq++}`;
}

// 工作流文档
const doc = reactive({
  id: "",
  name: "未命名工作流",
  description: "",
  enabled: true,
  nodes: [],
  edges: [],
});

const selectedNodeId = ref("");
const selectedEdgeId = ref("");
const selectedNode = computed(() =>
  doc.nodes.find((n) => n.id === selectedNodeId.value)
);
const selectedEdge = computed(() =>
  doc.edges.find((e) => e.id === selectedEdgeId.value)
);

// 连线交互：点击输出端口 -> 等待点击输入端口
const linkingFrom = ref("");

// 拖拽状态
const dragging = reactive({ id: "", dx: 0, dy: 0 });

const canvasRef = ref(null);

function addNode(type) {
  const count = doc.nodes.length;
  doc.nodes.push({
    id: newId("n"),
    type,
    name: type.charAt(0).toUpperCase() + type.slice(1),
    position: { x: 80 + (count % 4) * 140, y: 80 + Math.floor(count / 4) * 120 },
    config: {},
  });
}

function deleteSelectedNode() {
  if (!selectedNodeId.value) return;
  doc.edges = doc.edges.filter(
    (e) => e.from !== selectedNodeId.value && e.to !== selectedNodeId.value
  );
  doc.nodes = doc.nodes.filter((n) => n.id !== selectedNodeId.value);
  selectedNodeId.value = "";
}

function deleteSelectedEdge() {
  if (!selectedEdgeId.value) return;
  doc.edges = doc.edges.filter((e) => e.id !== selectedEdgeId.value);
  selectedEdgeId.value = "";
}

// 端口点击：输出端口开始连线，输入端口完成连线
function onPortClick(nodeId, kind) {
  if (kind === "out") {
    linkingFrom.value = nodeId;
    return;
  }
  // in
  if (linkingFrom.value && linkingFrom.value !== nodeId) {
    doc.edges.push({
      id: newId("e"),
      from: linkingFrom.value,
      to: nodeId,
      condition: doc.nodes.find((n) => n.id === linkingFrom.value)?.type === "condition" ? "true" : "",
    });
  }
  linkingFrom.value = "";
}

function startDrag(e, node) {
  const rect = canvasRef.value.getBoundingClientRect();
  dragging.id = node.id;
  dragging.dx = e.clientX - rect.left - node.position.x;
  dragging.dy = e.clientY - rect.top - node.position.y;
  window.addEventListener("mousemove", onDragMove);
  window.addEventListener("mouseup", stopDrag);
}

function onDragMove(e) {
  const node = doc.nodes.find((n) => n.id === dragging.id);
  if (!node) return;
  const rect = canvasRef.value.getBoundingClientRect();
  node.position.x = Math.max(0, e.clientX - rect.left - dragging.dx);
  node.position.y = Math.max(0, e.clientY - rect.top - dragging.dy);
}

function stopDrag() {
  dragging.id = "";
  window.removeEventListener("mousemove", onDragMove);
  window.removeEventListener("mouseup", stopDrag);
}

onBeforeUnmount(() => {
  // 组件卸载时若仍在拖拽，移除全局监听，避免泄漏。
  stopDrag();
});

function portPos(node, kind) {
  if (kind === "out") return { x: node.position.x + nodeSize, y: node.position.y + nodeSize / 2 };
  return { x: node.position.x, y: node.position.y + nodeSize / 2 };
}

const svgEdges = computed(() => {
  return doc.edges.map((e) => {
    const from = doc.nodes.find((n) => n.id === e.from);
    const to = doc.nodes.find((n) => n.id === e.to);
    if (!from || !to) return null;
    const p1 = portPos(from, "out");
    const p2 = portPos(to, "in");
    const mx = (p1.x + p2.x) / 2;
    return {
      id: e.id,
      d: `M ${p1.x} ${p1.y} C ${mx} ${p1.y}, ${mx} ${p2.y}, ${p2.x} ${p2.y}`,
      condition: e.condition || "",
      selected: e.id === selectedEdgeId.value,
    };
  }).filter(Boolean);
});

function selectNode(id) {
  selectedNodeId.value = id;
  selectedEdgeId.value = "";
}
function selectEdge(id) {
  selectedEdgeId.value = id;
  selectedNodeId.value = "";
}

// --- API 操作 ---
const saving = ref(false);
const executing = ref(false);
const execLog = ref([]);

async function save() {
  saving.value = true;
  try {
    const payload = JSON.parse(JSON.stringify(doc));
    if (payload.id) {
      await updateWorkflow(payload.id, payload);
      message.success("已保存");
    } else {
      const created = await createWorkflow(payload);
      doc.id = created.id;
      message.success("已创建并保存");
    }
  } catch (e) {
    message.error("保存失败：" + String(e.message || e));
  } finally {
    saving.value = false;
  }
}

async function loadList() {
  try {
    const res = await listWorkflows();
    return res.workflows || [];
  } catch {
    return [];
  }
}

async function loadSelected(id) {
  try {
    const wf = await getWorkflow(id);
    doc.id = wf.id;
    doc.name = wf.name;
    doc.description = wf.description;
    doc.enabled = wf.enabled;
    doc.nodes = wf.nodes || [];
    doc.edges = wf.edges || [];
    execLog.value = [];
    message.success("已加载：" + wf.name);
  } catch (e) {
    message.error("加载失败：" + String(e.message || e));
  }
}

async function newWorkflow() {
  doc.id = "";
  doc.name = "未命名工作流";
  doc.description = "";
  doc.enabled = true;
  doc.nodes = [];
  doc.edges = [];
  execLog.value = [];
}

async function removeWorkflow() {
  if (!doc.id) return;
  try {
    await deleteWorkflow(doc.id);
    message.success("已删除");
    await newWorkflow();
  } catch (e) {
    message.error("删除失败：" + String(e.message || e));
  }
}

async function execute() {
  if (!doc.id) {
    message.error("请先保存工作流再执行");
    return;
  }
  executing.value = true;
  execLog.value = [];
  try {
    const res = await executeWorkflow(doc.id, {});
    execLog.value = res.log || [];
    message.success(res.success ? "执行成功" : "执行结束（未到达 end）");
  } catch (e) {
    message.error("执行失败：" + String(e.message || e));
  } finally {
    executing.value = false;
  }
}

const workflows = ref([]);
async function refreshList() {
  workflows.value = await loadList();
}

onMounted(refreshList);

function onConfigKeyInput(node, key, value) {
  if (!node.config) node.config = {};
  if (value === "") {
    delete node.config[key];
  } else {
    node.config[key] = value;
  }
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3 overflow-hidden p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex flex-wrap items-center gap-2">
        <input
          v-model="doc.name"
          class="rounded border border-[#333] bg-[#1f1f1f] px-2 py-1 text-sm text-white outline-none"
          placeholder="工作流名称"
        />
        <Button variant="primary" :disabled="saving" @click="save">
          {{ saving ? "保存中..." : "保存" }}
        </Button>
        <Button variant="default" :disabled="executing" @click="execute">
          {{ executing ? "执行中..." : "执行" }}
        </Button>
        <Button variant="default" @click="newWorkflow">新建</Button>
        <Button v-if="doc.id" variant="default" @click="removeWorkflow">删除</Button>
        <span v-if="linkingFrom" class="text-xs text-[#f59e0b]">请点击目标节点输入端口完成连线…</span>
      </div>
    </Card>

    <div class="flex min-h-0 flex-1 gap-3">
      <!-- 左侧工具栏 -->
      <Card class="w-[180px] shrink-0">
        <div class="mb-2 text-sm text-[#a3a3a3]">添加节点</div>
        <div class="flex flex-col gap-2">
          <Button
            v-for="t in NODE_TYPES"
            :key="t"
            variant="default"
            @click="addNode(t)"
          >
            + {{ t }}
          </Button>
        </div>
        <div class="mt-4 border-t border-[#242424] pt-3">
          <div class="mb-2 text-sm text-[#a3a3a3]">已保存工作流</div>
          <div class="flex max-h-[200px] flex-col gap-1 overflow-y-auto">
            <button
              v-for="w in workflows"
              :key="w.id"
              class="truncate rounded px-2 py-1 text-left text-xs text-[#d4d4d4] hover:bg-[#1f1f1f]"
              :title="w.name"
              @click="loadSelected(w.id)"
            >
              {{ w.name }}
            </button>
            <div v-if="!workflows.length" class="text-xs text-[#666]">暂无</div>
          </div>
          <Button variant="default" class="mt-2 w-full" @click="refreshList">刷新列表</Button>
        </div>
      </Card>

      <!-- 画布 -->
      <Card class="relative min-h-0 flex-1 overflow-hidden p-0">
        <div
          ref="canvasRef"
          class="relative h-full w-full"
          style="background-image: radial-gradient(#2a2a2a 1px, transparent 1px); background-size: 20px 20px;"
        >
          <svg class="pointer-events-none absolute inset-0 h-full w-full">
            <g v-for="e in svgEdges" :key="e.id">
              <path
                :d="e.d"
                fill="none"
                :stroke="e.selected ? '#3b82f6' : '#888'"
                stroke-width="2"
                class="cursor-pointer"
                @click="selectEdge(e.id)"
              />
              <text
                v-if="e.condition"
                :x="(e.d.match(/C ([\d.]+)/) || [0,0])[1]"
                :y="e.d.split(' ').pop()"
                fill="#f59e0b"
                font-size="11"
              >{{ e.condition }}</text>
            </g>
          </svg>

          <div
            v-for="n in doc.nodes"
            :key="n.id"
            class="absolute cursor-move select-none rounded border-2 bg-[#232323] px-2 text-center text-xs text-white shadow"
            :style="{
              left: n.position.x + 'px',
              top: n.position.y + 'px',
              width: nodeSize + 'px',
              height: nodeSize + 'px',
              borderColor: NODE_COLORS[n.type],
            }"
            :class="{ 'ring-2 ring-[#3b82f6]': n.id === selectedNodeId }"
            @mousedown="startDrag($event, n)"
            @click.stop="selectNode(n.id)"
          >
            <div class="truncate font-medium">{{ n.name }}</div>
            <div class="text-[10px] text-[#aaa]">{{ n.type }}</div>
            <!-- 输入端口 -->
            <div
              class="absolute -left-2 top-1/2 h-3 w-3 -translate-y-1/2 rounded-full bg-[#888]"
              @click.stop="onPortClick(n.id, 'in')"
            ></div>
            <!-- 输出端口 -->
            <div
              class="absolute -right-2 top-1/2 h-3 w-3 -translate-y-1/2 rounded-full bg-[#10AD5D]"
              @click.stop="onPortClick(n.id, 'out')"
            ></div>
          </div>

          <div
            v-if="!doc.nodes.length"
            class="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 text-sm text-[#666]"
          >
            从左侧添加节点开始编排
          </div>
        </div>
      </Card>

      <!-- 右侧属性面板 -->
      <Card class="w-[260px] shrink-0 overflow-y-auto">
        <div v-if="selectedNode" class="flex flex-col gap-3">
          <div class="text-sm font-medium text-white">节点属性</div>
          <label class="text-xs text-[#a3a3a3]">名称</label>
          <input
            v-model="selectedNode.name"
            class="rounded border border-[#333] bg-[#1f1f1f] px-2 py-1 text-sm text-white outline-none"
          />
          <label class="text-xs text-[#a3a3a3]">类型</label>
          <div class="text-sm text-white">{{ selectedNode.type }}</div>
          <label class="text-xs text-[#a3a3a3]">配置 (key=value 逐行)</label>
          <div
            v-for="(val, key) in selectedNode.config"
            :key="key"
            class="flex items-center gap-1"
          >
            <span class="w-16 truncate text-[#aaa]">{{ key }}</span>
            <input
              :value="val"
              class="flex-1 rounded border border-[#333] bg-[#1f1f1f] px-1 py-0.5 text-xs text-white outline-none"
              @input="onConfigKeyInput(selectedNode, key, $event.target.value)"
            />
          </div>
          <input
            class="rounded border border-[#333] bg-[#1f1f1f] px-2 py-1 text-xs text-white outline-none"
            placeholder="新增 key=value"
            @keyup.enter="(e) => { const [k,v]=e.target.value.split('='); if(k) onConfigKeyInput(selectedNode, k, v||''); e.target.value=''; }"
          />
          <Button variant="default" @click="deleteSelectedNode">删除节点</Button>
        </div>

        <div v-else-if="selectedEdge" class="flex flex-col gap-3">
          <div class="text-sm font-medium text-white">连线属性</div>
          <label class="text-xs text-[#a3a3a3]">条件</label>
          <select
            v-model="selectedEdge.condition"
            class="rounded border border-[#333] bg-[#1f1f1f] px-2 py-1 text-sm text-white outline-none"
          >
            <option value="">无条件</option>
            <option value="true">true 分支</option>
            <option value="false">false 分支</option>
          </select>
          <Button variant="default" @click="deleteSelectedEdge">删除连线</Button>
        </div>

        <div v-else class="text-sm text-[#666]">选择节点或连线以编辑属性</div>
      </Card>
    </div>

    <!-- 执行日志 -->
    <Card v-if="execLog.length" class="shrink-0">
      <div class="mb-2 text-sm font-medium text-white">执行日志</div>
      <div class="max-h-[160px] overflow-y-auto font-mono text-xs text-[#d4d4d4]">
        <div v-for="(entry, i) in execLog" :key="i" class="border-b border-[#222] py-0.5">
          [{{ entry.nodeType }}] {{ entry.nodeName }} — {{ entry.action }}: {{ entry.detail }}
        </div>
      </div>
    </Card>
  </div>
</template>