<script setup>
import { ref, onMounted, computed } from "vue";
import Card from "@/components/ui/Card.vue";
import Button from "@/components/ui/Button.vue";
import CacheHitRateChart from "@/components/charts/CacheHitRateChart.vue";
import { getCacheStats, clearCache } from "@/services/clientApi";
import { showModal } from "@/composables/useModal";

const stats = ref(null);
const loading = ref(false);
const clearing = ref(false);
const error = ref("");

const hitRatePercent = computed(() => {
  const rate = Number(stats.value?.hitRate);
  if (!Number.isFinite(rate)) {
    return 0;
  }
  return rate;
});

const metricCards = computed(() => {
  const s = stats.value || {};
  return [
    { label: "精确命中", value: s.exactHits ?? 0 },
    { label: "语义命中", value: s.semanticHits ?? 0 },
    { label: "总命中", value: s.totalHits ?? 0 },
    { label: "总未命中", value: s.totalMisses ?? 0 },
    { label: "节省 Token", value: s.tokensSaved ?? 0 },
    { label: "缓存条目", value: s.entries ?? 0 },
  ];
});

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const result = await getCacheStats();
    stats.value = result || null;
  } catch (e) {
    error.value = String(e || "未知错误");
  } finally {
    loading.value = false;
  }
}

async function handleClear() {
  clearing.value = true;
  error.value = "";
  try {
    await clearCache();
    await load();
  } catch (e) {
    await showModal({
      title: "清空失败",
      content: String(e || "未知错误"),
    });
  } finally {
    clearing.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">缓存 Dashboard</h2>
          <div class="text-sm text-[#a3a3a3]">
            查看精确 / 语义缓存命中率、节省 Token 与缓存条目
          </div>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="primary" :disabled="loading" @click="load">
            {{ loading ? "加载中..." : "刷新" }}
          </Button>
          <Button variant="default" :disabled="clearing" @click="handleClear">
            {{ clearing ? "清空中..." : "清空缓存" }}
          </Button>
        </div>
      </div>
    </Card>

    <div v-if="error" class="text-sm text-[#f87171]">
      {{ error }}
    </div>

    <div v-if="stats" class="flex flex-col gap-4">
      <Card>
        <div class="flex items-center gap-6">
          <CacheHitRateChart :rate="hitRatePercent" />
          <div>
            <div class="text-sm text-[#a3a3a3]">当前命中率</div>
            <div class="text-2xl font-medium text-white">
              {{ (hitRatePercent * 100).toFixed(2) }}%
            </div>
          </div>
        </div>
      </Card>

      <div class="grid grid-cols-2 gap-4 md:grid-cols-3">
        <Card v-for="m in metricCards" :key="m.label">
          <div class="text-sm text-[#a3a3a3]">{{ m.label }}</div>
          <div class="mt-1 text-xl font-medium text-white">
            {{ m.value }}
          </div>
        </Card>
      </div>
    </div>

    <div v-else-if="!loading" class="text-sm text-[#a3a3a3]">
      暂无缓存数据。
    </div>
  </div>
</template>