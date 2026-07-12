<template>
  <MainLayout />
  <MessageProvider />
  <AdModelProvider v-if="isMainWindow" />
  <Modal
    :visible="activeModal.visible"
    :title="activeModal.title"
    :content="activeModal.content"
    :confirm-text="activeModal.confirmText"
    :cancel-text="activeModal.cancelText"
    :show-cancel="activeModal.showCancel"
    :confirm-disabled="activeModal.confirmDisabled"
    @confirm="activeModal.onConfirm"
    @cancel="activeModal.onCancel"
  />
  <InputModal
    :visible="inputModalState.visible"
    :title="inputModalState.title"
    :content="inputModalState.content"
    :placeholder="inputModalState.placeholder"
    :model-value="inputModalState.value"
    @update:model-value="inputModalState.value = $event"
    @confirm="resolveInputModal(true)"
    @cancel="resolveInputModal(false)"
  />
</template>
<script setup>
import { computed } from "vue";
import { useRoute } from "vue-router";
import MainLayout from "@/layouts/MainLayout.vue";
import AdModelProvider from "@/components/AdModelProvider.vue";
import Modal from "@/components/ui/Modal.vue";
import MessageProvider from "@/components/ui/MessageProvider.vue";
import { modalState, resolveModal } from "@/composables/useModal";
import InputModal from "@/components/ui/InputModal.vue";
import { inputModalState, resolveInputModal } from "@/composables/useInputModal";
import { appState, confirmUpdatePrompt, dismissUpdatePrompt, updateViewState } from "@/state/appState";

const route = useRoute();
const isMainWindow = computed(() => route.path === "/");

// 统一 Modal：优先显示更新提示，否则显示全局弹窗。
// 避免两个 Modal 实例同时可见导致 UI 冲突。
const activeModal = computed(() => {
  if (isMainWindow.value && appState.updatePromptVisible) {
    return {
      visible: true,
      title: updateViewState.promptTitle,
      content: updateViewState.promptContent,
      confirmText: updateViewState.promptConfirmText,
      cancelText: updateViewState.promptCancelText,
      showCancel: updateViewState.promptShowCancel,
      confirmDisabled: appState.updatePromptBusy,
      onConfirm: confirmUpdatePrompt,
      onCancel: dismissUpdatePrompt,
    };
  }
  return {
    visible: modalState.visible,
    title: modalState.title,
    content: modalState.content,
    confirmText: modalState.confirmText,
    cancelText: modalState.cancelText,
    showCancel: modalState.showCancel,
    confirmDisabled: modalState.confirmDisabled,
    onConfirm: () => resolveModal(true),
    onCancel: () => resolveModal(false),
  };
});
</script>
