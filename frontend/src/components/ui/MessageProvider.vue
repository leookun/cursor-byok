<script setup>
import { messageState, provideMessage } from "@/composables/useMessage";

provideMessage();
</script>

<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed inset-x-0 top-[calc(48px+env(safe-area-inset-top))] z-[11000] flex justify-center px-4">
      <Transition name="message-slide" mode="out-in">
        <div
          v-if="messageState.current"
          :key="messageState.current.id"
          role="status"
          aria-live="polite"
          class="max-w-[min(520px,calc(100vw-32px))] rounded-[8px] border border-[#454545] bg-[#2b2b2b] px-4 py-2.5 text-center text-sm leading-5 text-[#ededed] shadow-[0_10px_30px_rgba(0,0,0,0.38)]"
        >
          {{ messageState.current.content }}
        </div>
      </Transition>
    </div>
  </Teleport>
</template>

<style scoped>
.message-slide-enter-active,
.message-slide-leave-active {
  transition: transform 0.2s ease, opacity 0.2s ease;
}

.message-slide-enter-from {
  opacity: 0;
  transform: translateY(-8px);
}

.message-slide-enter-to,
.message-slide-leave-from {
  opacity: 1;
  transform: translateY(0);
}

.message-slide-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
