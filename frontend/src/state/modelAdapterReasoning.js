export const SUPPORTED_REASONING_EFFORTS = new Set(["", "low", "medium", "high", "xhigh", "max"]);

export function normalizeReasoningEffort(value) {
  if (typeof value === "string") {
    return value.trim().toLowerCase();
  }
  if (value instanceof String) {
    return value.toString().trim().toLowerCase();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value).trim().toLowerCase();
  }
  return "";
}
