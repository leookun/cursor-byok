export const GPT56_DEFAULT_CONTEXT = 272_000;
export const GPT56_LONG_CONTEXT = 1_000_000;

export function isGpt56LongContextModel(modelId: string): boolean {
  const id = modelId.toLowerCase().replaceAll("_", "-");
  return id.includes("gpt-5.6") && (id.includes("luna") || id.includes("sol") || id.includes("terra"));
}

export function contextWindowForModel(
  modelId: string,
  discovered: number | null | undefined,
  fallback: number | null,
): number | null {
  if (isGpt56LongContextModel(modelId)) {
    return fallback === GPT56_LONG_CONTEXT ? GPT56_LONG_CONTEXT : GPT56_DEFAULT_CONTEXT;
  }
  return discovered ?? fallback;
}
