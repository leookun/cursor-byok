import { reactive } from "vue";
import { localized } from "@/i18n/runtime";

export const modalState = reactive({
  visible: false,
  title: localized("f56c6c82203b33f6", "Notice").toString(),
  content: "",
  confirmText: localized("fac2a67ad87807c4", "OK").toString(),
  cancelText: localized("2cd0f3be8738a86c", "Cancel").toString(),
  showCancel: true,
  confirmDisabled: false,
  _resolve: null,
});

/**
 * 显示确认弹窗，返回 Promise<boolean>
 * @param {Object} options - { title, content }
 * @returns {Promise<boolean>} - true=确定, false=取消
 */
export function showModal(options = {}) {
  return new Promise((resolve) => {
    modalState.visible = true;
    modalState.title = options.title ?? localized("f56c6c82203b33f6", "Notice").toString();
    modalState.content = options.content ?? "";
    modalState.confirmText = options.confirmText ?? localized("fac2a67ad87807c4", "OK").toString();
    modalState.cancelText = options.cancelText ?? localized("2cd0f3be8738a86c", "Cancel").toString();
    modalState.showCancel = options.showCancel ?? true;
    modalState.confirmDisabled = options.confirmDisabled ?? false;
    modalState._resolve = resolve;
  });
}

export function resolveModal(ok) {
  modalState.visible = false;
  const resolve = modalState._resolve;
  modalState._resolve = null;
  resolve?.(ok);
}
