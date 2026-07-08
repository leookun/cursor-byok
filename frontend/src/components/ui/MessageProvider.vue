<script setup>
import { messageState, provideMessage } from "@/composables/useMessage";

provideMessage();

const MESSAGE_THEME = {
  success: {
    containerClass: "bg-[#10AD5D] text-white",
    iconClass: "icon-[dashicons--yes]",
    iconExtraClass: "",
  },
  error: {
    containerClass: "bg-[#D84C4C] text-white",
    iconClass: "",
    iconExtraClass: "",
  },
  info: {
    containerClass: "bg-[#F08A24] text-white",
    iconClass: "",
    iconExtraClass: "",
  },
  loading: {
    containerClass: "bg-[#3a3a3a] text-white",
    iconClass: "icon-[mingcute--loading-fill]",
    iconExtraClass: "animate-spin",
  },
};

function resolveTheme(type) {
  return MESSAGE_THEME[type] || MESSAGE_THEME.info;
}
</script>

<template>
  <div class="pointer-events-none fixed inset-x-0 top-4 z-[1000] flex justify-center px-4">
    <Transition name="message-slide" mode="out-in">
      <div
        v-if="messageState.current"
        :key="messageState.current.id"
        class="pointer-events-auto inline-flex max-w-full items-center gap-2 rounded-full px-4 py-2 text-sm shadow-[0_8px_24px_rgba(0,0,0,0.28)]"
        :class="resolveTheme(messageState.current.type).containerClass"
      >
        <span
          v-if="resolveTheme(messageState.current.type).iconClass"
          class="text-[14px]"
          :class="[
            resolveTheme(messageState.current.type).iconClass,
            resolveTheme(messageState.current.type).iconExtraClass,
          ]"
        />
        <span class="leading-none whitespace-nowrap">{{ messageState.current.content }}</span>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.message-slide-enter-active,
.message-slide-leave-active {
  transition: transform 0.2s ease, opacity 0.2s ease;
}

.message-slide-enter-from {
  opacity: 0;
  transform: translateY(-12px);
}

.message-slide-enter-to,
.message-slide-leave-from {
  opacity: 1;
  transform: translateY(0);
}

.message-slide-leave-to {
  opacity: 0;
  transform: translateY(-12px);
}
</style>





