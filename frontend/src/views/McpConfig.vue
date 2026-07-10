<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import Switch from "@/components/ui/Switch.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import {
  appState,
  applyCursorMcpConfig,
  createEmptyMcpServer,
  deleteMcpServerAt,
  duplicateMcpServerAt,
  mcpHubEndpoint,
  reloadUserConfig,
  saveMcpServerAt,
  testMcpServerConnection,
  toggleMcpServerAt,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, reactive, ref } from "vue";

const message = useMessage();

const endpoint = computed(() => mcpHubEndpoint());
const servers = computed(() => appState.mcpServers ?? []);

const editing = ref(null); // { index, form: { name, url, enabled } }
const editError = ref("");
const testResults = reactive({}); // key = server.name → { status, toolCount, durationMS, error }
const togglingName = ref("");

function asString(value) {
  return typeof value === "string" ? value.trim() : "";
}

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function copyEndpoint() {
  try {
    await navigator.clipboard.writeText(endpoint.value);
    message.success("已复制网关端点地址");
  } catch (_error) {
    message.error("复制失败，请手动选中地址复制");
  }
}

const applying = ref(false);

async function applyToCursor() {
  if (applying.value) {
    return;
  }
  const enabledCount = servers.value.filter((item) => item.enabled).length;
  const lines = [
    "将把 Cursor 的 mcp.json 重写为只包含本网关：",
    "",
    endpoint.value,
    "",
    "• 原 mcp.json 会自动备份到同目录 .bak 文件",
    "• Cursor mcp.json 里原有的其它 MCP 服务器会被移除——请改在本页「新增后端」聚合",
    "• 写入后需重启 Cursor 才能生效",
  ];
  if (enabledCount === 0) {
    lines.push("", "⚠ 当前网关内没有已启用的后端，写入后 Cursor 将看不到任何工具。");
  }
  const confirmed = await showModal({
    title: "一键配置到 Cursor",
    content: lines.join("\n"),
    confirmText: "确认写入",
    cancelText: "取消",
    showCancel: true,
  });
  if (!confirmed) {
    return;
  }
  applying.value = true;
  try {
    const result = await applyCursorMcpConfig();
    if (result?.ok) {
      const tip = result.backupPath ? "（原文件已备份）" : "";
      message.success(`已写入 Cursor mcp.json${tip}，重启 Cursor 生效`);
    } else {
      await showActionError("写入失败", result?.error || "未知错误");
    }
  } catch (error) {
    await showActionError("写入失败", toUserError(error));
  } finally {
    applying.value = false;
  }
}

function openEditor(index = -1) {
  if (index >= 0) {
    const source = servers.value[index];
    editing.value = {
      index,
      form: {
        name: source?.name ?? "",
        url: source?.url ?? "",
        enabled: source?.enabled ?? true,
      },
    };
  } else {
    editing.value = { index: -1, form: createEmptyMcpServer() };
  }
  editError.value = "";
}

function cancelEditor() {
  editing.value = null;
  editError.value = "";
}

async function saveEditor() {
  if (!editing.value) {
    return;
  }
  const form = editing.value.form;
  if (!asString(form.name)) {
    editError.value = "名称不能为空";
    return;
  }
  if (!asString(form.url)) {
    editError.value = "地址不能为空";
    return;
  }
  const result = await saveMcpServerAt(editing.value.index, form);
  if (!result.ok) {
    editError.value = result.error || "保存失败";
    return;
  }
  message.success("已保存 MCP 后端");
  editing.value = null;
  editError.value = "";
}

async function handleToggle(index, enabled) {
  const target = servers.value[index];
  if (!target) {
    return;
  }
  togglingName.value = target.name;
  try {
    const result = await toggleMcpServerAt(index, enabled);
    if (!result.ok) {
      await showActionError("切换失败", result.error);
    }
  } finally {
    togglingName.value = "";
  }
}

async function handleDelete(index) {
  const target = servers.value[index];
  if (!target) {
    return;
  }
  const result = await deleteMcpServerAt(index);
  if (!result.ok) {
    await showActionError("删除失败", result.error);
  }
}

async function handleDuplicate(index) {
  const target = servers.value[index];
  if (!target) {
    return;
  }
  const result = await duplicateMcpServerAt(index);
  if (!result.ok) {
    await showActionError("复制失败", result.error);
    return;
  }
  message.success("已复制后端（端口 +1、自动重命名）");
}

async function handleTest(server) {
  const key = server.name;
  testResults[key] = { status: "running" };
  try {
    const result = await testMcpServerConnection(server.url);
    if (result?.ok) {
      testResults[key] = {
        status: "success",
        toolCount: result.toolCount ?? 0,
        durationMS: result.durationMS ?? 0,
      };
    } else {
      testResults[key] = {
        status: "error",
        error: result?.error || "连接失败",
      };
    }
  } catch (error) {
    testResults[key] = { status: "error", error: toUserError(error) };
  }
}

function testSummary(server) {
  const result = testResults[server.name];
  if (!result) {
    return { text: "未测试", cls: "text-[#737373]" };
  }
  if (result.status === "running") {
    return { text: "测试中...", cls: "text-[#a3a3a3]" };
  }
  if (result.status === "success") {
    return {
      text: `连接正常 · ${result.toolCount} 个工具 · ${result.durationMS}ms`,
      cls: "text-[#10AD5D]",
    };
  }
  return { text: String(result.error || "连接失败"), cls: "text-[#fca5a5]" };
}

function isTesting(server) {
  return testResults[server.name]?.status === "running";
}

onMounted(async () => {
  await reloadUserConfig({ mcpServersOnly: true }).catch(() => {});
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3 p-4 pt-0 text-[#e5e5e5] overflow-hidden">
    <!-- 网关端点 + 说明 -->
    <Card class="shrink-0">
      <div class="flex flex-col gap-2">
        <div class="flex items-center justify-between gap-3">
          <h2 class="text-base font-medium text-white">MCP 网关端点</h2>
          <div class="center-row gap-2">
            <Button variant="default" @click="copyEndpoint">复制端点</Button>
            <Button variant="primary" :disabled="applying" @click="applyToCursor">
              {{ applying ? "写入中..." : "一键配置到 Cursor" }}
            </Button>
          </div>
        </div>
        <div
          class="select-all break-all rounded-[6px] border border-[#343434] bg-[#232323] px-3 py-2 font-mono text-sm text-[#d4d4d4]"
        >
          {{ endpoint }}
        </div>
        <p class="text-xs leading-[18px] text-[#8f8f8f]">
          在 Cursor 的 <span class="text-[#a3a3a3]">mcp.json</span> 里只保留一个
          <span class="text-[#a3a3a3]">type: "http"</span> 的服务器指向上面这个地址，即可聚合下面所有已启用的后端，突破单会话工具数量上限。
        </p>
      </div>
    </Card>

    <!-- 顶部操作 -->
    <div class="flex shrink-0 items-center justify-between gap-4">
      <div class="text-sm text-[#a3a3a3]">
        已聚合 {{ servers.length }} 个后端，其中启用
        {{ servers.filter((item) => item.enabled).length }} 个
      </div>
      <Button variant="primary" :disabled="appState.configSaving || !!editing" @click="openEditor()">
        新增后端
      </Button>
    </div>

    <!-- 内联编辑器 -->
    <Card v-if="editing" class="shrink-0">
      <div class="flex flex-col gap-3">
        <div class="text-sm font-medium text-white">
          {{ editing.index >= 0 ? "编辑 MCP 后端" : "新增 MCP 后端" }}
        </div>
        <div class="flex flex-col gap-1">
          <label class="text-xs text-[#8f8f8f]">名称（多开的同类后端用它区分，建议带语义，如 doge-LC）</label>
          <Input v-model="editing.form.name" placeholder="例如 doge-LC" />
        </div>
        <div class="flex flex-col gap-1">
          <label class="text-xs text-[#8f8f8f]">地址（后端 MCP 的 http 端点）</label>
          <Input v-model="editing.form.url" placeholder="例如 http://127.0.0.1:55556" />
        </div>
        <Switch
          compact
          label="启用"
          enabled-text="已启用（会被网关聚合）"
          disabled-text="已停用（网关会跳过）"
          :enabled="editing.form.enabled"
          @change="(value) => (editing.form.enabled = value)"
        />
        <div v-if="editError" class="text-xs text-[#fca5a5]">{{ editError }}</div>
        <div class="center-row justify-end gap-2">
          <Button variant="text" :disabled="appState.configSaving" @click="cancelEditor">取消</Button>
          <Button variant="primary" :disabled="appState.configSaving" @click="saveEditor">保存</Button>
        </div>
      </div>
    </Card>

    <!-- 后端列表 -->
    <div class="min-h-0 flex-1">
      <div
        v-if="servers.length === 0"
        class="flex h-full min-h-[180px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        还没有配置任何 MCP 后端，点击右上角「新增后端」添加。
      </div>

      <div v-else class="h-full min-h-0 overflow-y-auto pr-1">
        <div class="grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]">
          <Card v-for="(server, index) in servers" :key="`${server.name}-${index}`">
            <div class="flex h-full min-h-[150px] flex-col justify-between gap-3">
              <div class="flex flex-col gap-2">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-base font-medium text-white" :title="server.name">
                      {{ server.name }}
                    </div>
                    <div class="mt-1 truncate font-mono text-xs text-[#8f8f8f]" :title="server.url">
                      {{ server.url }}
                    </div>
                  </div>
                  <Switch
                    compact
                    :enabled="server.enabled"
                    :busy="togglingName === server.name"
                    enabled-text=""
                    disabled-text=""
                    busy-text=""
                    @change="(value) => handleToggle(index, value)"
                  />
                </div>

                <div class="rounded-[8px] bg-[#232323] px-3 py-2">
                  <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">连接测试</div>
                  <div class="mt-1 truncate text-xs" :class="testSummary(server).cls" :title="testSummary(server).text">
                    {{ testSummary(server).text }}
                  </div>
                </div>
              </div>

              <div class="center-row flex-wrap justify-end gap-2 border-t border-[#343434] pt-3">
                <Button
                  variant="default"
                  :disabled="appState.configSaving || isTesting(server)"
                  @click="handleTest(server)"
                >
                  {{ isTesting(server) ? "测试中..." : "测试" }}
                </Button>
                <Button variant="default" :disabled="appState.configSaving || !!editing" @click="openEditor(index)">
                  编辑
                </Button>
                <Button variant="default" :disabled="appState.configSaving || !!editing" @click="handleDuplicate(index)">
                  复制
                </Button>
                <Button variant="text" :disabled="appState.configSaving" @click="handleDelete(index)">删除</Button>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  </div>
</template>
