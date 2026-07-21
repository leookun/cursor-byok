<script setup>
import { ref, onMounted } from "vue";
import { useMessage } from "@/composables/useMessage";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import {
  listPlugins,
  installPlugin,
  uninstallPlugin,
  togglePlugin,
  callPlugin,
} from "@/services/pluginApi";

// Phase 8 切片：Plugin Marketplace（接入 host 运行时 + 后端 API）。
const message = useMessage();

const loading = ref(false);
const plugins = ref([]);
const callInputText = ref("{}");
const callResult = ref(null);
const activeCallName = ref("");

async function load() {
  loading.value = true;
  try {
    const data = await listPlugins();
    plugins.value = data.plugins || [];
  } catch (e) {
    message.error("加载插件列表失败：" + (e?.message || e));
  } finally {
    loading.value = false;
  }
}

async function doInstall(p) {
  try {
    await installPlugin(p.name);
    message.success(`已安装 ${p.name}`);
    await load();
  } catch (e) {
    message.error("安装失败：" + (e?.message || e));
  }
}

async function doUninstall(p) {
  try {
    await uninstallPlugin(p.name);
    message.success(`已卸载 ${p.name}`);
    await load();
  } catch (e) {
    message.error("卸载失败：" + (e?.message || e));
  }
}

async function doToggle(p) {
  try {
    const res = await togglePlugin(p.name);
    // 更新本地状态
    p.enabled = res.enabled;
    message.success(res.enabled ? `已启用 ${p.name}` : `已禁用 ${p.name}`);
  } catch (e) {
    message.error("切换失败：" + (e?.message || e));
  }
}

async function doCall(p) {
  activeCallName.value = p.name;
  callResult.value = null;
  let input = {};
  const raw = (callInputText.value || "").trim();
  if (raw) {
    try {
      input = JSON.parse(raw);
    } catch (e) {
      message.error("调用参数不是合法 JSON：" + (e?.message || e));
      activeCallName.value = "";
      return;
    }
  }
  try {
    const res = await callPlugin(p.name, input);
    callResult.value = res.result;
    message.success(`调用 ${p.name} 成功`);
  } catch (e) {
    callResult.value = { error: e?.message || String(e) };
    message.error("调用失败：" + (e?.message || e));
  } finally {
    activeCallName.value = "";
  }
}

onMounted(load);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">插件市场（Plugin Marketplace）</h2>
          <div class="text-sm text-[#a3a3a3]">
            安装 / 卸载 / 启用第三方 Virtual Model 与 Tool 扩展（Phase 8）
          </div>
        </div>
        <Button size="sm" :loading="loading" @click="load">刷新</Button>
      </div>
    </Card>

    <Card>
      <div class="mb-3 text-sm font-medium text-white">可用插件</div>
      <div v-if="loading" class="text-sm text-[#777]">加载中…</div>
      <div v-else-if="plugins.length === 0" class="text-sm text-[#777]">暂无插件。</div>
      <div v-else class="flex flex-col gap-2">
        <div
          v-for="p in plugins"
          :key="p.name"
          class="flex flex-col gap-2 rounded-md bg-[#1f1f1f] px-3 py-2"
        >
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium text-white">{{ p.name }}</span>
                <span class="rounded bg-[#333] px-1.5 py-0.5 text-xs text-[#aaa]">v{{ p.version }}</span>
                <span
                  class="rounded px-1.5 py-0.5 text-xs"
                  :class="p.installed ? (p.enabled ? 'bg-green-900 text-green-300' : 'bg-yellow-900 text-yellow-300') : 'bg-[#333] text-[#aaa]'"
                >
                  {{ p.installed ? (p.enabled ? "已启用" : "已禁用") : "未安装" }}
                </span>
              </div>
              <div class="mt-0.5 truncate text-xs text-[#777]">
                source: {{ p.source || "builtin" }} · installedAt:
                {{ p.installedAt ? new Date(p.installedAt).toLocaleString() : "-" }}
              </div>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <template v-if="!p.installed">
                <Button size="sm" variant="primary" @click="doInstall(p)">安装</Button>
              </template>
              <template v-else>
                <Button size="sm" @click="doToggle(p)">{{ p.enabled ? "禁用" : "启用" }}</Button>
                <Button size="sm" variant="text" @click="doUninstall(p)">卸载</Button>
                <Button size="sm" :loading="activeCallName === p.name" @click="doCall(p)">调用</Button>
              </template>
            </div>
          </div>

          <div v-if="p.installed" class="flex items-center gap-2 border-t border-[#2a2a2a] pt-2">
            <input
              v-model="callInputText"
              placeholder="调用参数 JSON，如 {text:hi}"
              class="flex-1 rounded bg-[#111] px-2 py-1 text-xs text-[#ddd] outline-none"
              @keyup.enter="doCall(p)"
            />
            <Button size="sm" :loading="activeCallName === p.name" @click="doCall(p)">调用</Button>
          </div>
        </div>
      </div>
    </Card>

    <Card v-if="callResult !== null">
      <div class="mb-2 text-sm font-medium text-white">调用结果</div>
      <pre class="overflow-x-auto rounded bg-[#111] p-2 text-xs text-[#ddd]">{{ JSON.stringify(callResult, null, 2) }}</pre>
    </Card>
  </div>
</template>