import assert from "node:assert/strict";
import test from "node:test";

import {
  normalizeReasoningEffort,
  SUPPORTED_REASONING_EFFORTS,
} from "./modelAdapterReasoning.js";

test("normalizeReasoningEffort preserves blank and supported values", () => {
  assert.equal(normalizeReasoningEffort(""), "");
  assert.equal(normalizeReasoningEffort(" HIGH "), "high");
  assert.equal(SUPPORTED_REASONING_EFFORTS.has(normalizeReasoningEffort("max")), true);
});

test("normalizeReasoningEffort preserves unknown values for validation", () => {
  const normalized = normalizeReasoningEffort(" Unsupported ");

  assert.equal(normalized, "unsupported");
  assert.equal(SUPPORTED_REASONING_EFFORTS.has(normalized), false);
});
