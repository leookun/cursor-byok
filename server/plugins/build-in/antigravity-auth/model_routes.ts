import type { ModelDefinition, ModelSnapshot } from "cursor-byok:model";

type CursorEffort = "low" | "medium" | "high";
type Route = { id: string; tier: string; thinkingBudget: number; maxOutputTokens: number };
type ModelRouting = { routes?: unknown };

/** 同一公开模型的上游变体只在这些档位后缀上不同。 */
const TIER_SUFFIXES = ["low", "medium", "high", "tiered", "base", "thinking"] as const;

/** 默认档位优先级:越靠前越接近官方默认选择。 */
const TIER_RANK: Record<string, number> = {
  high: 0,
  thinking: 1,
  medium: 2,
  tiered: 3,
  low: 4,
  base: 5,
};

/** 宿主固定提供这五档;文档未公开的档位饱和到同家族最接近的一档。 */
const EFFORT_RANK: Record<string, number> = { low: 0, medium: 1, high: 2, xhigh: 2, max: 2 };

function normalize(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]/g, "");
}

/** 拆出档位后缀:"gemini-3.8-flash-high" -> { name: "gemini-3.8-flash", tier: "high" }。 */
function splitTier(value: string): { name: string; tier: string } {
  const cleaned = value.replace(/\([^)]*\)/g, "").trim();
  const lower = cleaned.toLowerCase();
  for (const tier of TIER_SUFFIXES) {
    if (lower.length > tier.length && lower.endsWith(tier)) {
      return { name: cleaned.slice(0, -tier.length).replace(/[\s._-]+$/, ""), tier };
    }
  }
  return { name: cleaned, tier: "base" };
}

/** 版本键:公开模型名与上游模型 ID 归一后必须相等(大小写、点线、"(thinking)" 都不算差异)。 */
function versionKey(value: string): string {
  return normalize(splitTier(value).name);
}

function effortForTier(tier: string): CursorEffort | undefined {
  if (tier === "low") return "low";
  if (tier === "medium" || tier === "tiered") return "medium";
  if (tier === "high" || tier === "thinking") return "high";
  return undefined;
}

/**
 * 账号真实目录 ∩ 官方公开清单:同名变体合并成一个可选模型,
 * 顺序跟随文档,只保留真实发现的上游路由。
 */
export function callableModels(
  discovered: ModelDefinition[],
  publicNames: string[],
): ModelDefinition[] {
  const publicOrder = new Map<string, number>();
  publicNames.forEach((name, index) => {
    const key = versionKey(name);
    if (!publicOrder.has(key)) publicOrder.set(key, index);
  });

  const families = new Map<
    string,
    { displayName: string; routes: Route[]; images: boolean; order: number }
  >();
  for (const model of discovered) {
    const { name, tier } = splitTier(model.id);
    const key = normalize(name);
    const order = publicOrder.get(key);
    if (order === undefined) continue;
    const data = model.privateData as { thinkingBudget?: number } | null;
    const family = families.get(key) ?? {
      displayName: publicNames[order],
      routes: [],
      images: true,
      order,
    };
    family.images = family.images && model.capabilities?.images === true;
    family.routes.push({
      id: model.id,
      tier,
      thinkingBudget: data?.thinkingBudget ?? 0,
      maxOutputTokens: model.maxOutputTokens ?? 65535,
    });
    families.set(key, family);
  }

  return [...families.values()]
    .sort((left, right) => left.order - right.order)
    .map((family) => {
      const routes = [...new Map(family.routes.map((route) => [route.id, route])).values()]
        .sort((left, right) =>
          TIER_RANK[left.tier] - TIER_RANK[right.tier] || left.id.localeCompare(right.id)
        );
      return {
        id: splitTier(routes[0].id).name,
        displayName: family.displayName,
        description: `Upstream variants: ${routes.map((route) => route.id).join(", ")}`,
        capabilities: { images: family.images },
        maxOutputTokens: routes[0].maxOutputTokens,
        privateData: { routes },
      };
    });
}

function isRoute(value: unknown): value is Route {
  const route = value as Route | null;
  return typeof route?.id === "string" && typeof route.tier === "string" &&
    typeof route.thinkingBudget === "number" && typeof route.maxOutputTokens === "number";
}

/** 从同步时持久化的路由里按 Cursor 的档位选真实上游 ID;绝不跨版本替换。 */
export function resolveModelRoute(model: ModelSnapshot, effort: string | null): Route {
  const data = model.privateData as ModelRouting | null;
  const stored = Array.isArray(data?.routes) ? data.routes : [];
  const routes = stored.filter(isRoute);
  const keys = new Set(routes.map((route) => versionKey(route.id)));
  if (routes.length !== stored.length || keys.size !== 1 || !keys.has(versionKey(model.id))) {
    throw new Error(
      "Antigravity model catalog is outdated; sync the model catalog before calling",
    );
  }

  if (effort !== null) {
    const requested = EFFORT_RANK[effort];
    if (requested === undefined) throw new Error(`Unsupported reasoning effort: ${effort}`);
    const ranked = routes
      .filter((item) => effortForTier(item.tier) !== undefined)
      .sort((left, right) => {
        const leftRank = EFFORT_RANK[effortForTier(left.tier)!];
        const rightRank = EFFORT_RANK[effortForTier(right.tier)!];
        return Math.abs(leftRank - requested) - Math.abs(rightRank - requested) ||
          rightRank - leftRank ||
          TIER_RANK[left.tier] - TIER_RANK[right.tier];
      });
    if (ranked.length > 0) return ranked[0];
  }
  return [...routes].sort((left, right) =>
    TIER_RANK[left.tier] - TIER_RANK[right.tier] || left.id.localeCompare(right.id)
  )[0];
}
