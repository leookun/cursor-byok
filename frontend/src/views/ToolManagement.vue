<script setup>
import { ref, onMounted, computed } from "vue";
import Card from "@/components/ui/Card.vue";
import Button from "@/components/ui/Button.vue";
import {
  listTools,
  getToolCacheStats,
  toggleTool,
  listMCPServers,
  toggleMCPServer,
  clearToolCache,
} from "@/services/clientApi";
import { showModal } from "@/composables/useModal";

const tools = ref([]);
const cacheStats = ref(null);
const mcpServers = ref([]);
const loading = ref(false);
const error = ref("");

const categoryLabels = {
  filesystem: "文件系统",
  mcp: "MCP",
  browser: "浏览器",
  shell: "Shell",
  git: "Git",
  search: "搜索",
};

function formatTTL(ttl) {
  if (!ttl) return "";
  // ttl is a Go duration string like "5m0s" or "30s"; simplify for display.
  const match = ttl.match(/^(\d+h)?(\d+m)?(\d+s)?$/);
  if (!match) return ttl;
  const parts = [];
  if (match[1]) parts.push(match[1]);
  if (match[2]) parts.push(match[2]);
  if (match[3]) parts.push(match[3]);
  return parts.join("") || ttl;
}

function formatSchema(schema) {
  if (!schema) return "无参数定义";
  try {
    return JSON.stringify(JSON.parse(schema), null, 2);
  } catch {
    return String(schema);
  }
}

async function showToolSchema(tool) {
  const title = `${tool.name} 参数`;
  const content = `内部名: ${tool.internalName || "—"}\n分类: ${categoryLabels[tool.category] || tool.category}\n描述: ${tool.description || "—"}\n\n${formatSchema(tool.schema)}`;
  await showModal({ title, content });
}

const enabledCount = computed(() => tools.value.filter((t) => t.enabled).length);

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const [toolList, stats, mcpList] = await Promise.all([
      listTools(),
      getToolCacheStats(),
      listMCPServers(),
    ]);
    tools.value = toolList || [];
    cacheStats.value = stats || null;
    mcpServers.value = mcpList || [];
  } catch (e) {
    error.value = String(e);
  } finally {
    loading.value = false;
  }
}

async function handleClearToolCache() {
  try {
    await clearToolCache();
    await load();
  } catch (e) {
    await showModal({
      title: "清空缓存失败",
      content: String(e || "未知错误"),
    });
  }
}

async function handleToggle(tool) {
  try {
    await toggleTool(tool.name, !tool.enabled);
    tool.enabled = !tool.enabled;
  } catch (e) {
    await showModal({
      title: "操作失败",
      content: String(e || "未知错误"),
    });
  }
}

async function handleToggleMCPServer(server) {
  const newEnabled = !server.enabled;
  try {
    await toggleMCPServer(server.name, newEnabled);
    server.enabled = newEnabled;
  } catch (e) {
    await showModal({
      title: "操作失败",
      content: String(e || "未知错误"),
    });
  }
}

onMounted(load);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]"
  >
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">工具管理</h2>
          <div class="text-sm text-[#a3a3a3]">
            已注册 {{ tools.length }} 个工具，{{ enabledCount }} 个启用
          </div>
        </div>
        <Button variant="primary" :disabled="loading" @click="load">
          {{ loading ? "加载中..." : "刷新" }}
        </Button>
      </div>
    </Card>

    <!-- 缓存统计 -->
    <Card v-if="cacheStats">
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <h2 class="text-base font-medium text-white">结果缓存</h2>
          <Button variant="secondary" size="sm" :disabled="loading" @click="handleClearToolCache">
            清空缓存
          </Button>
        </div>
        <div class="grid grid-cols-4 gap-3 text-sm">
          <div class="rounded bg-[#1f1f1f] p-2">
            <div class="text-[#a3a3a3]">命中</div>
            <div class="text-lg font-medium text-white">{{ cacheStats.hits }}</div>
          </div>
          <div class="rounded bg-[#1f1f1f] p-2">
            <div class="text-[#a3a3a3]">未命中</div>
            <div class="text-lg font-medium text-white">{{ cacheStats.misses }}</div>
          </div>
          <div class="rounded bg-[#1f1f1f] p-2">
            <div class="text-[#a3a3a3]">缓存条目</div>
            <div class="text-lg font-medium text-white">{{ cacheStats.entries }}</div>
          </div>
          <div class="rounded bg-[#1f1f1f] p-2">
            <div class="text-[#a3a3a3]">命中率</div>
            <div class="text-lg font-medium text-white">
              {{ (cacheStats.hitRate * 100).toFixed(1) }}%
            </div>
          </div>
        </div>
      </div>
    </Card>

    <!-- MCP Servers -->
    <Card v-if="mcpServers.length > 0">
      <div class="flex flex-col gap-3">
        <h2 class="text-base font-medium text-white">MCP 服务器</h2>
        <div
          v-for="sv in mcpServers"
          :key="sv.name"
          class="flex items-center justify-between gap-3 rounded-md bg-[#1f1f1f] px-3 py-2"
        >
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-white">{{ sv.name }}</span>
              <span class="rounded bg-[#333] px-1.5 py-0.5 text-xs text-[#aaa]">
                {{ sv.toolCount }} 个工具
              </span>
              <span
                v-if="sv.enabledTool > 0 && sv.enabledTool < sv.toolCount"
                class="rounded bg-yellow-500/20 px-1.5 py-0.5 text-xs text-yellow-400"
              >
                部分启用 ({{ sv.enabledTool }}/{{ sv.toolCount }})
              </span>
            </div>
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              :checked="sv.enabled"
              class="h-4 w-4 rounded border-[#404040] bg-[#171717] text-[#3b82f6]"
              @change="handleToggleMCPServer(sv)"
            />
            <span class="text-[#a3a3a3]">{{ sv.enabled ? "启用" : "禁用" }}</span>
          </label>
        </div>
      </div>
    </Card>

    <!-- 错误提示 -->
    <div
      v-if="error"
      class="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400"
    >
      {{ error }}
    </div>

    <!-- 工具列表 -->
    <Card v-if="tools.length > 0">
      <div class="flex flex-col gap-2">
        <div
          v-for="tool in tools"
          :key="tool.name"
          class="flex cursor-pointer items-center justify-between gap-3 rounded-md bg-[#1f1f1f] px-3 py-2 hover:bg-[#2a2a2a]"
          @click="showToolSchema(tool)"
        >
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-white">{{ tool.name }}</span>
              <span
                class="rounded bg-[#333] px-1.5 py-0.5 text-xs text-[#aaa]"
              >
                {{ categoryLabels[tool.category] || tool.category }}
              </span>
              <span
                v-if="tool.cacheable"
                class="rounded bg-blue-500/20 px-1.5 py-0.5 text-xs text-blue-400"
              >
                可缓存{{ tool.cacheTTL ? " (" + formatTTL(tool.cacheTTL) + ")" : "" }}
              </span>
            </div>
            <div class="mt-0.5 truncate text-xs text-[#777]">
              {{ tool.description || tool.internalName || "—" }}
            </div>
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-xs"
            @click.stop
          >
            <input
              type="checkbox"
              :checked="tool.enabled"
              class="h-4 w-4 rounded border-[#404040] bg-[#171717] text-[#3b82f6]"
              @change="handleToggle(tool)"
            />
            <span class="text-[#a3a3a3]">{{ tool.enabled ? "启用" : "禁用" }}</span>
          </label>
        </div>
      </div>
    </Card>

    <!-- 空状态 -->
    <Card v-if="!loading && tools.length === 0 && !error">
      <div class="py-4 text-center text-sm text-[#a3a3a3]">
        暂无已注册工具，启动服务后自动注册
      </div>
    </Card>
  </div>
</template>