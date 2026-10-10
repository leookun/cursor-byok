import assert from "node:assert/strict";
import test from "node:test";
import { createSingleFlight, mergePluginSnapshots, applyPluginSelection } from "../src/shared/store/pluginState.ts";
import { remainingSeconds, showsMetricDeadline, takeNewRefreshTargets } from "../src/features/plugins/accountLifecycle.ts";
import { createPluginFixture } from "../src/demo/pluginFixture.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

test("automatic refresh consumes IDs once per manager, not per snapshot or page", () => {
  const fixture = createPluginFixture(1000, 0);
  const resources = fixture.plugins[0].resources;
  const seen = new Set<string>();
  assert.equal(takeNewRefreshTargets(seen, resources).length, 12);
  assert.equal(takeNewRefreshTargets(seen, structuredClone(resources)).length, 0);
  const id = fixture.addAccount();
  assert.deepEqual(takeNewRefreshTargets(seen, resources), [{ type: "accounts", id }]);
  assert.equal(takeNewRefreshTargets(seen, resources).length, 0);
  assert.equal(takeNewRefreshTargets(new Set(), resources).length, 13);
});

test("capabilities govern refresh for all providers and types remain distinct", () => {
  const fixture = createPluginFixture(1000, 0);
  for (const plugin of fixture.plugins) assert.ok(takeNewRefreshTargets(new Set(), plugin.resources).length > 0);
  const resource = fixture.plugins[0].resources[0];
  assert.equal(takeNewRefreshTargets(new Set(), [{ ...resource, canRefresh: false }]).length, 0);
  assert.equal(takeNewRefreshTargets(new Set(), [resource, { ...resource, type: "other" }]).length, 24);
});

test("StrictMode/reopen joins in-flight refresh without preventing subsequent manual retry", async () => {
  const run = createSingleFlight();
  const pending = deferred<number>();
  let count = 0;
  const task = () => { count++; return pending.promise; };
  const first = run("account", task);
  const reopened = run("account", task);
  assert.equal(first, reopened);
  await Promise.resolve();
  assert.equal(count, 1);
  pending.resolve(7);
  assert.equal(await reopened, 7);
  await run("account", task);
  assert.equal(count, 2);
});

test("failed refresh is retryable and independent accounts remain concurrent", async () => {
  const run = createSingleFlight();
  const blocked = deferred<number>();
  const first = run("one", () => blocked.promise);
  assert.equal(await run("two", async () => 2), 2);
  await assert.rejects(run("failure", async () => { throw new Error("offline"); }), /offline/);
  assert.equal(await run("failure", async () => 3), 3);
  blocked.resolve(1);
  assert.equal(await first, 1);
});

test("authoritative selection survives slow snapshots while newer revisions are adopted", () => {
  const fixture = createPluginFixture(1000, 0);
  const previous = structuredClone(fixture.plugins);
  const selected = applyPluginSelection(previous, "fixture-codex", "accounts", { activeResourceId: "account-2", automaticSwitching: false, revision: 5 });
  const merged = mergePluginSnapshots(selected, fixture.plugins);
  assert.deepEqual(merged[0].resources[0].selection, selected[0].resources[0].selection);
  fixture.plugins[0].resources[0].selection = { activeResourceId: "account-4", automaticSwitching: true, revision: 6 };
  assert.equal(mergePluginSnapshots(merged, fixture.plugins)[0].resources[0].selection.activeResourceId, "account-4");
  assert.equal(previous[0].resources[0].selection.revision, 1);
});

test("late PUT response cannot replace a newer polled selection", () => {
  const fixture = createPluginFixture(1000, 0);
  fixture.plugins[0].resources[0].selection.revision = 10;
  const updated = applyPluginSelection(fixture.plugins, "fixture-codex", "accounts", { activeResourceId: "account-2", automaticSwitching: false, revision: 9 });
  assert.equal(updated[0].resources[0].selection.activeResourceId, "account-12");
});

test("countdown handles missing/invalid timestamps and clamps elapsed time without changing quota", () => {
  assert.equal(remainingSeconds(null, 1000), null);
  assert.equal(remainingSeconds(undefined, 1000), null);
  assert.equal(remainingSeconds(NaN, 1000), null);
  assert.equal(remainingSeconds(1001, 1000), 1);
  assert.equal(remainingSeconds(1000, 1000), 0);
  assert.equal(remainingSeconds(999, 1000), 0);
});

test("Rust nullable metric fields show only the relevant unknown deadline", () => {
  const quota = { id: "weekly", label: "Quota", unit: "percent" as const, value: 50, resetAtMs: null, expiresAtMs: null };
  const cards = { ...quota, id: "cards", unit: "count" as const, value: 2 };
  assert.equal(showsMetricDeadline(quota, "reset"), true);
  assert.equal(showsMetricDeadline(quota, "expiry"), false);
  assert.equal(showsMetricDeadline(cards, "reset"), false);
  assert.equal(showsMetricDeadline(cards, "expiry"), true);
  assert.equal(showsMetricDeadline({ ...quota, expiresAtMs: undefined }, "expiry"), false);
  assert.equal(showsMetricDeadline({ ...cards, resetAtMs: undefined }, "reset"), false);
});

test("explicit timestamps remain visible regardless of metric unit, including epoch zero", () => {
  const quota = { id: "weekly", label: "Quota", unit: "percent" as const, value: 50, resetAtMs: 1000, expiresAtMs: null };
  const cards = { id: "cards", label: "Reset cards", unit: "count" as const, value: 2, resetAtMs: null, expiresAtMs: 1000 };
  assert.equal(showsMetricDeadline(quota, "expiry"), false);
  assert.equal(showsMetricDeadline(cards, "reset"), false);
  assert.equal(showsMetricDeadline({ ...quota, expiresAtMs: 0 }, "expiry"), true);
  assert.equal(showsMetricDeadline({ ...cards, resetAtMs: 0 }, "reset"), true);
});

test("isolated fixture exercises successful and rejected selection without real accounts", async () => {
  const fixture = createPluginFixture(1000, 0);
  assert.equal(fixture.plugins[0].providers[0].configured, false);
  const url = "/plugins/fixture-codex/resources/accounts/selection";
  const response = await fixture.handle(url, "PUT", { activeResourceId: "account-2", automaticSwitching: false });
  assert.equal(response?.status, 200);
  assert.deepEqual(await response?.json(), { activeResourceId: "account-2", automaticSwitching: false, revision: 2 });
  const rejected = await fixture.handle(url, "PUT", { activeResourceId: "account-3", automaticSwitching: true });
  assert.equal(rejected?.status, 409);
  assert.equal(fixture.plugins[0].resources[0].selection.activeResourceId, "account-2");
  assert.equal(fixture.plugins[0].resources[0].selection.automaticSwitching, false);
});
