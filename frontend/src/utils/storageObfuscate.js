/**
 * 轻量级 localStorage 混淆 —— 防止 API Key 等敏感数据明文落盘。
 * 不是密码学加密（密钥硬编码在源码中），但足以阻止 casual 浏览与文本扫描。
 *
 * NOTE: 理想方案是将 API Key 交后端管理，前端仅存 token；当前方案为短期改进。
 */

const KEY_SEED = "cursor-byok-local-obfuscate-v1";

function xorEncode(input, seed) {
  let result = "";
  for (let i = 0; i < input.length; i++) {
    result += String.fromCharCode(
      input.charCodeAt(i) ^ seed.charCodeAt(i % seed.length),
    );
  }
  return result;
}

/** 混淆字符串 → base64（适合 localStorage 存储） */
export function obfuscate(plainText) {
  const xored = xorEncode(plainText, KEY_SEED);
  // 用 btoa 编码（注意 btoa 仅处理 Latin-1，XOR 后可能出现超出范围的字符）
  // 安全做法：先转 UTF-8 字节再 btoa
  const encoder = new TextEncoder();
  const bytes = encoder.encode(xored);
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

/** 反向解析 base64 → 原始字符串 */
export function deobfuscate(encoded) {
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  const decoder = new TextDecoder();
  const xored = decoder.decode(bytes);
  return xorEncode(xored, KEY_SEED);
}
