import { fetchPublicModelNames, parsePublicModelNames } from "./public_models.ts";

function assertEquals(actual: unknown, expected: unknown): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) throw new Error(`expected ${right}, received ${left}`);
}

const DOCS_PAGE = `# Models

## Reasoning Model

| Model | Free & Google AI Plus | Google AI Pro |
| --- | --- | --- |
| [Gemini 3.8 Flash](/blog/gemini-3-8-flash-in-google-antigravity) | ✅ | ✅ |
| Claude Sonnet 4.6 (thinking) | ✅ | ✅ |
| GPT-OSS-120b | ✅ | ❌ |

## Additional Models

Antigravity uses a number of other models that are not customizable.
`;

Deno.test("public model names come from the official docs table", () => {
  assertEquals(parsePublicModelNames(DOCS_PAGE), [
    "Gemini 3.8 Flash",
    "Claude Sonnet 4.6 (thinking)",
    "GPT-OSS-120b",
  ]);
});

Deno.test("a docs page without the model table fails instead of returning an empty list", () => {
  let rejected = false;
  try {
    parsePublicModelNames("# Models\n\nNo table here.\n");
  } catch {
    rejected = true;
  }
  assertEquals(rejected, true);
});

Deno.test("fetching the public list reports upstream HTTP failures", async () => {
  let requestedUrl = "";
  const names = await fetchPublicModelNames({
    fetch: (url) => {
      requestedUrl = url;
      return Promise.resolve({ status: 200, headers: {}, body: DOCS_PAGE });
    },
    stream: () => Promise.reject(new Error("stream is not expected")),
  });
  assertEquals(names.length, 3);
  assertEquals(requestedUrl, "https://antigravity.google/docs/models.md");

  let rejected = false;
  try {
    await fetchPublicModelNames({
      fetch: () => Promise.resolve({ status: 503, headers: {}, body: "unavailable" }),
      stream: () => Promise.reject(new Error("stream is not expected")),
    });
  } catch {
    rejected = true;
  }
  assertEquals(rejected, true);
});
