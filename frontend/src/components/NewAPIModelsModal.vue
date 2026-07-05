<script setup>
import NewAPIModelsPanel from "@/components/NewAPIModelsPanel.vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
});
const emit = defineEmits(["update:visible", "close", "imported"]);

function handleClose() {
  emit("close");
  emit("update:visible", false);
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="fixed inset-0 z-999 flex items-center justify-center bg-black/55 p-4"
        @click.self="handleClose"
      >
        <Transition name="modal-content">
          <div
            v-show="visible"
            class="relative z-10 h-[min(86vh,860px)] w-full max-w-[1080px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="flex h-full min-h-0 flex-col rounded-[7px] bg-[#292929] p-5">
              <div class="mb-4 flex items-center justify-between gap-4">
                <div>
                  <h3 class="text-base font-medium text-white">选择要导入的 NewAPI 模型</h3>
                  <div class="text-sm text-[#a3a3a3]">弹层展示，不离开当前账号页面</div>
                </div>
                <button class="rounded-[6px] p-2 text-[#a3a3a3] hover:bg-[#1f1f1f] hover:text-white" @click="handleClose">
                  <span class="icon-[mdi--close] text-[18px]"></span>
                </button>
              </div>
              <NewAPIModelsPanel class="min-h-0 flex-1" @close="handleClose" @imported="$emit('imported', $event)" />
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-mask-enter-active,
.modal-mask-leave-active {
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}
.modal-content-enter-active,
.modal-content-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.modal-content-enter-from,
.modal-content-leave-to {
  opacity: 0;
  transform: scale(0.95) translateY(-8px);
}
</style>
