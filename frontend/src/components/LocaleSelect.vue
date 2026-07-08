<script setup>
import Select from "@/components/ui/Select.vue";
import { useLocale } from "@/i18n/runtime";

const props = defineProps({
  border: { type: Boolean, default: true },
  ariaLabel: { type: String, default: "界面语言" },
  buttonClass: { type: String, default: "" },
  menuClass: { type: String, default: "" },
  wrapperClass: { type: String, default: "w-[180px] max-w-full" },
  placeholder: { type: String, default: "选择语言" },
});

const { locale, localeOptions, setLocale } = useLocale();
</script>

<template>
  <div :class="wrapperClass">
    <Select
      :model-value="locale"
      :options="localeOptions"
      :border="border"
      :aria-label="ariaLabel"
      :button-class="buttonClass"
      :menu-class="menuClass"
      :placeholder="placeholder"
      @update:model-value="setLocale"
    />
  </div>
</template>
