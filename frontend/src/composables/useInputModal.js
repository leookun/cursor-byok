import { reactive } from "vue";
import { localized } from "@/i18n/runtime";

export const inputModalState = reactive({
  visible: false,
  title: localized("f56c6c82203b33f6", "Notice").toString(),
  content: "",
  placeholder: "",
  value: "",
  _resolve: null,
});

/**
 * 显示输入弹窗，返回 Promise<string|null>
 * @param {Object} options - { title, content, placeholder, defaultValue }
 * @returns {Promise<string|null>} - string=确定后的输入值, null=取消
 */
export function showInputModal(options = {}) {
  return new Promise((resolve) => {
    inputModalState.visible = true;
    inputModalState.title = options.title ?? localized("f56c6c82203b33f6", "Notice").toString();
    inputModalState.content = options.content ?? "";
    inputModalState.placeholder = options.placeholder ?? "";
    inputModalState.value = String(options.defaultValue ?? "");
    inputModalState._resolve = resolve;
  });
}

export function resolveInputModal(ok) {
  const value = String(inputModalState.value ?? "").trim();
  inputModalState.visible = false;
  inputModalState._resolve?.(ok ? value : null);
  inputModalState._resolve = null;
  if (!ok) {
    inputModalState.value = "";
  }
}
