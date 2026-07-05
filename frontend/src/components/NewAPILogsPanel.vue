<script setup>
import Button from "@/components/ui/Button.vue";
import { showModal } from "@/composables/useModal";
import { newAPIGetLogs } from "@/services/clientApi";
import { toUserError } from "@/state/appState";
import { onMounted, ref } from "vue";

const emit = defineEmits(["close"]);

const logsLoading = ref(false);
const logsData = ref({ records: [], total: 0, page: 1, size: 20 });

async function fetchLogs(page) {
  logsLoading.value = true;
  try {
    logsData.value = await newAPIGetLogs({ page, size: 20 });
  } catch (error) {
    await showModal({
      title: "获取使用记录失败",
      content: toUserError(error),
    });
  } finally {
    logsLoading.value = false;
  }
}

function formatLogTime(raw) {
  if (!raw) return "";
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return raw;
  const pad = (n) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function formatLogQuota(quota) {
  const usd = Number(quota ?? 0) / 500000;
  return `$${usd.toFixed(4)}`;
}

function formatTokens(val) {
  const n = new Intl.NumberFormat("en-US");
  return n.format(Number(val ?? 0));
}

function handleClose() {
  emit("close");
}

onMounted(() => {
  void fetchLogs(1);
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-3">
    <div class="flex shrink-0 items-center justify-between gap-4">
      <div>
        <h2 class="text-base font-medium text-white">使用记录</h2>
        <div class="text-sm text-[#a3a3a3]">查看最近的模型调用记录与费用</div>
      </div>
      <div class="center-row gap-2">
        <span v-if="logsLoading" class="text-xs text-[#8f8f8f]">加载中...</span>
        <Button variant="default" :disabled="logsLoading" @click="fetchLogs(logsData.page)">刷新</Button>
        <Button variant="text" @click="handleClose">关闭</Button>
      </div>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <div
        v-if="logsData.records.length === 0 && !logsLoading"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        暂无使用记录
      </div>

      <table
        v-else
        class="w-full text-sm border-collapse"
      >
        <thead>
          <tr class="text-left text-xs text-[#8f8f8f] border-b border-[#3f3f3f]">
            <th class="py-2 pr-3 font-normal">时间</th>
            <th class="py-2 pr-3 font-normal">模型</th>
            <th class="py-2 pr-3 font-normal text-right">Prompt</th>
            <th class="py-2 pr-3 font-normal text-right">Completion</th>
            <th class="py-2 font-normal text-right">费用</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="record in logsData.records"
            :key="record.id"
            class="border-b border-[#2a2a2a] text-xs"
          >
            <td class="py-2 pr-3 whitespace-nowrap text-[#8f8f8f]">
              {{ formatLogTime(record.created_at) }}
            </td>
            <td class="py-2 pr-3 truncate max-w-[150px]">
              {{ record.model_name }}
            </td>
            <td class="py-2 pr-3 text-right text-[#a3a3a3]">
              {{ formatTokens(record.prompt_tokens) }}
            </td>
            <td class="py-2 pr-3 text-right text-[#a3a3a3]">
              {{ formatTokens(record.completion_tokens) }}
            </td>
            <td class="py-2 text-right text-[#10AD5D]">
              {{ formatLogQuota(record.quota) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div
      v-if="logsData.total > logsData.size"
      class="flex shrink-0 items-center justify-center gap-3 pt-2 text-xs text-[#8f8f8f]"
    >
      <button
        :disabled="logsData.page <= 1 || logsLoading"
        class="cursor-pointer hover:text-[#e5e5e5] disabled:cursor-not-allowed disabled:opacity-40"
        @click="fetchLogs(logsData.page - 1)"
      >
        上一页
      </button>
      <span>第 {{ logsData.page }} 页 / 共 {{ Math.ceil(logsData.total / logsData.size) }} 页</span>
      <button
        :disabled="logsData.page >= Math.ceil(logsData.total / logsData.size) || logsLoading"
        class="cursor-pointer hover:text-[#e5e5e5] disabled:cursor-not-allowed disabled:opacity-40"
        @click="fetchLogs(logsData.page + 1)"
      >
        下一页
      </button>
    </div>
  </div>
</template>