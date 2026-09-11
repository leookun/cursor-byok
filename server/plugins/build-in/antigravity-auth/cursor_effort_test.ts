import { callableModels, resolveModelRoute } from "./model_routes.ts";

const PUBLIC_NAMES = [
  "Claude Sonnet 4.6 (thinking)",
  "Claude Opus 4.6 (thinking)",
  "GPT-OSS-120b",
  "Gemini 3.1 Pro",
];

Deno.test("Cursor's fixed effort menu remains usable for a single discovered family route", () => {
  for (
    const id of [
      "claude-sonnet-4-6",
      "claude-opus-4-6-thinking",
      "gpt-oss-120b-medium",
      "gemini-3.1-pro-low",
    ]
  ) {
    const model = callableModels([{ id, displayName: id }], PUBLIC_NAMES)[0];
    for (const effort of ["low", "medium", "high", "xhigh", "max"]) {
      const selected = resolveModelRoute(model, effort);
      if (selected.id !== id) {
        throw new Error(`unexpected model substitution: ${id} -> ${selected.id}`);
      }
    }
  }
});
