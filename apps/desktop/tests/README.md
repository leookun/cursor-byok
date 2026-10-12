# Plugin account tests

`npm run test:plugins` uses Node's built-in test runner (Node 22.6+), with no extra dependencies. It covers refresh identity/lifetime, in-flight deduplication and retries, selection revision ordering, countdown boundaries, and the isolated fixture API.

## Browser fixture

From `apps/desktop`, run:

```sh
npm run start -- --config vite.demo.config.ts --host 127.0.0.1 --port 4179 --strictPort
```

Open `http://127.0.0.1:4179/product-demo/demo/index.html?locale=en-US#/plugins`.

The demo intercepts management API requests in memory. It never calls the management server or real account providers. The production entry point does not import the fixture. Use `locale=zh-CN` or `locale=pt-BR`, `theme=default-light`, and `fixtureDelay=3000` as needed.

The three fixture plugins all support quota refresh and have saved accounts but failed model synchronization. Codex has 12 accounts, with account 12 selected outside the first page. Account 2 is cooling, account 3 deliberately rejects refresh and selection, and account 4 has unknown timestamps. Remaining values are retained at elapsed countdowns.

The demo exposes `window.pluginFixture` for browser test control:

- `calls` records method/path for every plugin request. Opening Codex management should issue one refresh POST per account, including under StrictMode. Further 5-second ticks only issue local GET snapshots.
- `addAccount()` adds a synthetic account. The next local snapshot should reveal it and trigger exactly one automatic refresh for that new ID.
- `setSnapshotFailure(true)` simulates local snapshot failure; existing rows and values should remain visible. Set it back to `false` to recover on the next tick.
- `setSelectionFailure(true)` rejects both account selection and automatic switching; the displayed selection must remain unchanged.
- `plugins` contains mutable fixture snapshots. Update selection and increment its `revision` to simulate automatic server switching; it should appear on the next poll.

Verify search/page changes do not refresh accounts; explicit manual refresh does. A new opening starts a new refresh lifecycle, while a rapid close/reopen during an unfinished request shares that in-flight request. Closing the manager, switching away from the cached route, or hiding the document stops polling. Late completions may update shared server snapshots but must not reopen closed UI.
