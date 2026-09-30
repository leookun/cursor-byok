import { modelMetadata } from "./model.ts";

function equal(actual: unknown, expected: unknown) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}`);
  }
}

Deno.test("model metadata copies explicit upstream limits and capabilities", () => {
  equal(modelMetadata({ context_window: 128_000, supports_tools: false, input_modalities: ["text", "image"] }), {
    contextWindowTokens: 128_000, capabilities: { tools: false, images: true },
  });
  equal(modelMetadata({ contextWindow: "64000", supportsTools: true, supportsImages: false }), {
    contextWindowTokens: 64_000, capabilities: { tools: true, images: false },
  });
  equal(modelMetadata({ max_prompt_length: 256_000, capabilities: { tools: true } }), {
    contextWindowTokens: 256_000, capabilities: { tools: true },
  });
});

Deno.test("missing or invalid metadata stays unknown instead of inferred from model ID", () => {
  equal(modelMetadata({ id: "gemini-claude-gpt", context_window: -1 }), { capabilities: {} });
  equal(modelMetadata({ contextWindow: 1.2, supportsImages: "true", supportsTools: null }), { capabilities: {} });
  equal(modelMetadata({ input_modalities: ["text"] }), { capabilities: { images: false } });
});
