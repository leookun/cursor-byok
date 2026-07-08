<script setup>
import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { computed, nextTick, onBeforeUnmount, ref, watch, watchPostEffect } from "vue";
import { localized } from "@/i18n/runtime";

const props = defineProps({
  modelValue: { type: String, default: "" },
  options: {
    type: Array,
    default: () => [],
  },
  placeholder: { type: String, default: localized("a1b2c3d4e5f6009b", "Please select").toString() },
  disabled: { type: Boolean, default: false },
  border: { type: Boolean, default: true },
  ariaLabel: { type: String, default: "" },
  buttonClass: { type: String, default: "" },
  menuClass: { type: String, default: "" },
});

const emit = defineEmits(["update:modelValue", "change", "blur"]);

const rootRef = ref(null);
const buttonRef = ref(null);
const menuRef = ref(null);
const optionRefs = ref([]);
const isOpen = ref(false);
const activeIndex = ref(-1);
const menuStyle = ref({});

const normalizedOptions = computed(() => props.options.map((option) => {
  if (typeof option === "string") {
    return { label: option, value: option };
  }

  return {
    label: option?.label ?? option?.value ?? "",
    value: option?.value ?? "",
    icon: option?.icon ?? option?.iconClass ?? "",
  };
}));

const selectedOption = computed(() => normalizedOptions.value.find((option) => option.value === props.modelValue) ?? null);
const selectedLabel = computed(() => selectedOption.value?.label || props.placeholder);

function setOptionRef(el, index) {
  if (el) {
    optionRefs.value[index] = el;
    return;
  }

  delete optionRefs.value[index];
}

function focusActiveOption() {
  nextTick(() => {
    const option = optionRefs.value[activeIndex.value];
    option?.focus();
  });
}

function openMenu() {
  if (props.disabled || isOpen.value) {
    return;
  }

  isOpen.value = true;
  const selectedIndex = normalizedOptions.value.findIndex((option) => option.value === props.modelValue);
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : 0;
  nextTick(() => {
    updatePosition();
    focusActiveOption();
  });
}

function closeMenu({ restoreFocus = false } = {}) {
  if (!isOpen.value) {
    return;
  }

  isOpen.value = false;
  activeIndex.value = -1;
  optionRefs.value = [];
  menuStyle.value = {};

  if (restoreFocus) {
    nextTick(() => buttonRef.value?.focus());
  }

  emit("blur");
}

function toggleMenu() {
  if (isOpen.value) {
    closeMenu();
    return;
  }

  openMenu();
}

function selectOption(option) {
  if (!option || option.value === props.modelValue) {
    closeMenu({ restoreFocus: true });
    return;
  }

  emit("update:modelValue", option.value);
  emit("change", option.value);
  closeMenu({ restoreFocus: true });
}

function moveActiveIndex(step) {
  if (!normalizedOptions.value.length) {
    return;
  }

  if (!isOpen.value) {
    openMenu();
    return;
  }

  const total = normalizedOptions.value.length;
  const current = activeIndex.value >= 0 ? activeIndex.value : 0;
  activeIndex.value = (current + step + total) % total;
  focusActiveOption();
}

function handleButtonKeydown(event) {
  if (props.disabled) {
    return;
  }

  switch (event.key) {
    case "ArrowDown":
      event.preventDefault();
      moveActiveIndex(1);
      break;
    case "ArrowUp":
      event.preventDefault();
      moveActiveIndex(-1);
      break;
    case "Enter":
    case " ":
      event.preventDefault();
      toggleMenu();
      break;
    case "Escape":
      if (isOpen.value) {
        event.preventDefault();
        closeMenu();
      }
      break;
    default:
      break;
  }
}

function handleOptionKeydown(event, option, index) {
  switch (event.key) {
    case "ArrowDown":
      event.preventDefault();
      activeIndex.value = index;
      moveActiveIndex(1);
      break;
    case "ArrowUp":
      event.preventDefault();
      activeIndex.value = index;
      moveActiveIndex(-1);
      break;
    case "Enter":
    case " ":
      event.preventDefault();
      selectOption(option);
      break;
    case "Escape":
      event.preventDefault();
      closeMenu({ restoreFocus: true });
      break;
    case "Tab":
      closeMenu();
      break;
    default:
      break;
  }
}

function handlePointerDown(event) {
  if (rootRef.value?.contains(event.target) || menuRef.value?.contains(event.target)) {
    return;
  }

  closeMenu();
}

function updatePosition() {
  if (!buttonRef.value || !menuRef.value) {
    return;
  }

  computePosition(buttonRef.value, menuRef.value, {
    placement: "bottom-start",
    middleware: [
      offset(6),
      flip({ padding: 12 }),
      shift({ padding: 12 }),
      size({
        apply({ rects, elements, availableHeight }) {
          Object.assign(elements.floating.style, {
            minWidth: `${rects.reference.width}px`,
            maxHeight: `${Math.max(availableHeight, 160)}px`,
          });
        },
        padding: 12,
      }),
    ],
  }).then(({ x, y }) => {
    menuStyle.value = {
      left: `${x}px`,
      top: `${y}px`,
    };
  });
}

watchPostEffect((cleanup) => {
  if (!isOpen.value || !buttonRef.value || !menuRef.value) {
    return;
  }

  const stopAutoUpdate = autoUpdate(buttonRef.value, menuRef.value, updatePosition);

  cleanup(() => {
    stopAutoUpdate();
  });
});

watch(() => props.modelValue, () => {
  if (!isOpen.value) {
    return;
  }

  const selectedIndex = normalizedOptions.value.findIndex((option) => option.value === props.modelValue);
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : 0;
});

watch(isOpen, (open) => {
  if (open) {
    document.addEventListener("pointerdown", handlePointerDown);
    return;
  }

  document.removeEventListener("pointerdown", handlePointerDown);
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", handlePointerDown);
});
</script>

<template>
  <div ref="rootRef" class="relative">
    <button
      ref="buttonRef"
      type="button"
      :disabled="disabled"
      class="flex h-9 items-center rounded-[6px] bg-[#232323] px-3 text-left text-sm text-[#e5e5e5] outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-60"
      :class="[
        border
          ? 'w-full justify-between gap-2 border border-[#3f3f3f] focus:border-[#10AD5D]'
          : 'w-auto justify-start gap-2 border border-transparent focus-visible:ring-2 focus-visible:ring-[#10AD5D]/35',
        buttonClass,
      ]"
      :aria-expanded="isOpen"
      :aria-label="ariaLabel || undefined"
      aria-haspopup="listbox"
      @click="toggleMenu"
      @keydown="handleButtonKeydown"
    >
      <span
        class="flex min-w-0 items-center gap-2"
        :class="[
          border ? 'flex-1' : 'shrink-0',
          selectedOption
            ? (border ? 'text-[#e5e5e5]' : 'text-current')
            : 'text-[#7b7b7b]',
        ]"
      >
        <span v-if="selectedOption?.icon" :class="[selectedOption.icon, 'text-[16px] shrink-0']" aria-hidden="true"></span>
        <span class="truncate">{{ selectedLabel }}</span>
      </span>
      <span
        class="pointer-events-none center-row transition-transform duration-200"
        :class="[border ? 'text-[#8f8f8f]' : 'text-current', isOpen ? 'rotate-180' : '']"
      >
        <span class="icon-[mdi--chevron-down] text-[18px]"></span>
      </span>
    </button>
  </div>

  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="translate-y-1 opacity-0"
      enter-to-class="translate-y-0 opacity-100"
      leave-active-class="transition duration-100 ease-in"
      leave-from-class="translate-y-0 opacity-100"
      leave-to-class="translate-y-1 opacity-0"
    >
      <div
        v-if="isOpen"
        ref="menuRef"
        class="fixed z-[999] overflow-hidden rounded-[8px] border border-[#3f3f3f] bg-[#232323] p-1 shadow-[0_16px_30px_-12px_rgba(0,0,0,0.7)]"
        :class="menuClass"
        :style="menuStyle"
      >
        <ul role="listbox" class="overflow-y-auto py-1">
          <li v-for="(option, index) in normalizedOptions" :key="option.value">
            <button
              :ref="(el) => setOptionRef(el, index)"
              type="button"
              role="option"
              class="flex w-full items-center rounded-[6px] px-3 py-2 text-left text-sm outline-none transition-colors"
              :class="[
                option.value === modelValue
                  ? 'bg-[#10AD5D]/15 text-[#10d06f]'
                  : 'text-[#e5e5e5] hover:bg-[#303030]',
                activeIndex === index ? 'bg-[#303030]' : '',
              ]"
              :aria-selected="option.value === modelValue"
              tabindex="0"
              @click="selectOption(option)"
              @mouseenter="activeIndex = index"
              @keydown="handleOptionKeydown($event, option, index)"
            >
              <span class="flex min-w-0 items-center gap-2">
                <span v-if="option.icon" :class="[option.icon, 'text-[16px] shrink-0']" aria-hidden="true"></span>
                <span class="truncate">{{ option.label }}</span>
              </span>
            </button>
          </li>
        </ul>
      </div>
    </Transition>
  </Teleport>
</template>
