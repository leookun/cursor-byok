<script setup>
defineProps({
  variant: {
    type: String,
    default: "default",
    validator: (v) => ["default", "primary", "text"].includes(v),
  },
});
</script>

<template>
  <button
    v-if="variant === 'text'"
    type="button"
    class="!whitespace-nowrap shrink-0 cursor-pointer text-sm text-[#a3a3a3] transition-colors duration-150 active:text-[#10AD5D] hover:text-[#10ad5cd9]"
  >
    <slot />
  </button>
  <button
    v-else
    type="button"
    class="!whitespace-nowrap relative cursor-pointer overflow-hidden center-row min-h-[24px] gap-[2px] rounded-[6px]  text-sm transition-transform duration-150 active:scale-105"
    :class="{
      'bg-[linear-gradient(to_bottom,#656565_0%,#3A3A3A_10px,#3A3A3A_100%)]': variant === 'default',
      'bg-gradient-to-b from-[#1D8010] to-[#25B433]': variant === 'primary',
    }"
  >
    <span
      class="relative center-row z-10 w-full justify-center rounded-[5px] !px-[7px] py-[3px] text-white transition-colors"
      :class="{
        'bg-gradient-to-b from-[#2a2a2a] to-[#1f1f1f] ': variant === 'default',
        'font-medium bg-gradient-to-b from-[#10AD5D] to-[#0F8A4C] ': variant === 'primary',
      }"
    >
      <slot />
    </span>
  </button>
</template>
