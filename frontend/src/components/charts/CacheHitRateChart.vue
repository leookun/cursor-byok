<script setup>
import {
  ArcElement,
  Chart as ChartJS,
  Tooltip,
} from "chart.js";
import { computed } from "vue";
import { Doughnut } from "vue-chartjs";
import { localized } from "@/i18n/runtime";

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
  const radius = 5;

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
  labels: [localized("a1b2c3d4e5f60120", "Hit").toString(), localized("a1b2c3d4e5f60121", "Miss").toString()],
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
  <div class="flex flex-col items-center gap-3">
    <div
      class="relative h-[82px] w-[132px] shrink-0"
      role="img"
      :aria-label="`${$ls('a1b2c3d4e5f60122', 'Cache hit rate')} ${label}`"
    >
      <Doughnut class="h-full w-full" :data="chartData" :options="chartOptions" />
      <div class="pointer-events-none absolute inset-x-0 bottom-[10px] flex justify-center">
        <div
          class="text-[20px] leading-none text-white"
          style="font-family: var(--font-num)"
        >
          {{ label }}
        </div>
      </div>
    </div>
  </div>
</template>
