// formatModelHost 从 baseURL 中提取主机名用于紧凑展示，无法解析时去掉协议前缀原样返回。
export function formatModelHost(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "";
  }
  try {
    const parsed = new URL(text);
    return parsed.host || text;
  } catch {
    return text.replace(/^https?:\/\//, "");
  }
}

// maskSecret 对访问密钥做脱敏，仅保留首尾少量字符，空值返回空串。
export function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "";
  }
  if (text.length <= 8) {
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  }
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}
