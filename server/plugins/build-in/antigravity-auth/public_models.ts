import type { PluginContext } from "cursor-byok:plugin";

/**
 * 公开可推理模型的唯一来源:官方文档页的 markdown 版本。
 * Antigravity 上下架模型后,重新同步模型即可跟随,插件不需要硬编码清单。
 */
export const PUBLIC_MODELS_URL = "https://antigravity.google/docs/models.md";

const MODEL_TABLE_HEADER = /^\|\s*model\s*\|/i;
const MARKDOWN_LINK = /\[([^\]]*)\]\([^)]*\)/g;

/** 解析文档中第一张 Model 表的数据行,得到公开模型名(如 "Gemini 3.8 Flash")。 */
export function parsePublicModelNames(markdown: string): string[] {
  const lines = markdown.split("\n").map((line) => line.trim());
  const header = lines.findIndex((line) => MODEL_TABLE_HEADER.test(line));
  if (header < 0) {
    throw new Error(`Antigravity public model list has no model table: ${PUBLIC_MODELS_URL}`);
  }
  const names: string[] = [];
  for (const line of lines.slice(header + 2)) {
    if (!line.startsWith("|")) break;
    const name = line.slice(1).split("|")[0].replace(MARKDOWN_LINK, "$1").trim();
    if (name) names.push(name);
  }
  if (names.length === 0) {
    throw new Error(`Antigravity public model list is empty: ${PUBLIC_MODELS_URL}`);
  }
  return names;
}

export async function fetchPublicModelNames(
  network: PluginContext["network"],
): Promise<string[]> {
  const response = await network.fetch(PUBLIC_MODELS_URL, {
    method: "GET",
    headers: { accept: "text/markdown" },
  });
  if (response.status < 200 || response.status >= 300) {
    throw new Error(
      `Antigravity public model list is unavailable (HTTP ${response.status})`,
    );
  }
  return parsePublicModelNames(response.body);
}
