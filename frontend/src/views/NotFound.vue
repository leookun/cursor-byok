<template>
  <div class="flex h-full flex-col items-center justify-center gap-4 p-8 text-[#e5e5e5]">
    <div class="text-6xl font-bold text-[#6f6f6f]">404</div>
    <div class="text-lg text-[#a3a3a3]">页面未找到</div>
    <button
      class="mt-2 rounded-lg bg-[#7c5cfc] px-6 py-2 text-sm text-white transition-colors hover:bg-[#6a4de6]"
      @click="$router.push('/')"
    >
      返回首页
    </button>
  </div>
</template>
