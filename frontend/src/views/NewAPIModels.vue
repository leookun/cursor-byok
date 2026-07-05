<script setup>
import NewAPIModelsPanel from "@/components/NewAPIModelsPanel.vue";
import { Window } from "@wailsio/runtime";

async function handleClose() {
  await Window.current().Close();
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden p-4 pt-0 text-[#e5e5e5]">
    <NewAPIModelsPanel @close="handleClose" @imported="handleClose" />
  </div>
</template>