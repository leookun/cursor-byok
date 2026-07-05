<script setup>
import NewAPILogsPanel from "@/components/NewAPILogsPanel.vue";
import { Window } from "@wailsio/runtime";

async function handleClose() {
  await Window.current().Close();
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden p-4 pt-0 text-[#e5e5e5]">
    <NewAPILogsPanel @close="handleClose" />
  </div>
</template>