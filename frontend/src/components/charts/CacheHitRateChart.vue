<script setup>
import {
  ArcElement,
  Chart as ChartJS,
  Tooltip,
} from "chart.js";
import { computed } from "vue";
import { Doughnut } from "vue-chartjs";

ChartJS.register(ArcElement, Tooltip);

const props = defineProps({
  rate: {
    type: Number,
    default: 0,
  },
});

const percentage = computed(() => {
  const rate = Number(props.rate);
  if (!Number.isFinite(rate)) {
    return 0;
  }
  return Math.max(0, Math.min(100, rate * 100));
});

const label = computed(() => {
  const rate = Number(props.rate);
  if (!Number.isFinite(rate)) {
    return "--";
  }
  return `${percentage.value.toFixed(2)}%`;
});

function getSegmentBorderRadius(dataIndex) {
  const radius = 3;

  if (percentage.value <= 0) {
    return dataIndex === 1
      ? {
          outerStart: radius,
          outerEnd: radius,
          innerStart: radius,
          innerEnd: radius,
        }
      : 0;
  }

  if (percentage.value >= 100) {
    return dataIndex === 0
      ? {
          outerStart: radius,
          outerEnd: radius,
          innerStart: radius,
          innerEnd: radius,
        }
      : 0;
  }

  return dataIndex === 0
    ? {
        outerStart: radius,
        outerEnd: 0,
        innerStart: radius,
        innerEnd: 0,
      }
    : {
        outerStart: 0,
        outerEnd: radius,
        innerStart: 0,
        innerEnd: radius,
      };
}

const chartData = computed(() => ({
  labels: ["命中", "未命中"],
  datasets: [
    {
      data: [percentage.value, Math.max(0, 100 - percentage.value)],
      backgroundColor: ["#4ade80", "#373737"],
      borderWidth: 0,
      hoverBorderWidth: 0,
      selfJoin: false,
      borderRadius: ({ dataIndex }) => getSegmentBorderRadius(dataIndex),
    },
  ],
}));

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  cutout: "82%",
  rotation: -90,
  circumference: 180,
  animation: {
    duration: 450,
  },
  events: [],
  plugins: {
    legend: {
      display: false,
    },
    tooltip: {
      enabled: false,
    },
  },
};
</script>

<template>
  <div class="flex flex-col items-center gap-1">
    <div
      class="relative h-[60px] w-[100px] shrink-0"
      role="img"
      :aria-label="`缓存命中率 ${label}`"
    >
      <Doughnut class="h-full w-full" :data="chartData" :options="chartOptions" />
      <div class="pointer-events-none absolute inset-x-0 bottom-[6px] flex justify-center">
        <div
          class="text-[16px] leading-none text-white"
          style="font-family: var(--font-num)"
        >
          {{ label }}
        </div>
      </div>
    </div>
  </div>
</template>
