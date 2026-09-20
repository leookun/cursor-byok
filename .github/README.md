# Desktop builds and releases

Push `feat/antigravity-models-and-quota` to `zhanpoint/cursor-byok` to build and publish a Beta release automatically:

For a newly forked repository, first open its Actions page and select **I understand my workflows, go ahead and enable them**. Workflow API status can already say `active` while this repository-level fork switch still blocks all runs. Push after enabling it; earlier push events are not replayed.

```text
Branch push
  -> Prepare a draft for the exact pushed commit
  -> Reuse ci.yml: Rust checks/tests, frontend checks, Antigravity tests
  -> Build in parallel on GitHub-hosted runners
     - Ubuntu x86_64: Linux bundles, including AppImage
     - Windows x86_64: NSIS installer (.exe)
     - macOS ARM64: .dmg and application archive
     - macOS Intel: .dmg and application archive
  -> Publish the Release only after every platform succeeds
```

The release version is `<desktop version>-beta.<GitHub run number>`, for example `1.0.1-beta.12`. Tauri receives this version through a build-only configuration overlay; developers do not need to bump the source version for each push. The generated tag points to the pushed commit. Published versions are never overwritten, and failed runs leave a draft that can be completed by rerunning the same run.

Branch releases are normal GitHub Releases with Beta in the version and release notes. Each completed release becomes Latest in the fork. The branch trigger is restricted to `zhanpoint/cursor-byok` so merging this workflow upstream cannot activate fork releases there. Publication requires a push by the repository owner.

Branch builds need no additional signing secret. They disable Tauri updater artifacts and application update checks; install subsequent branch versions manually. Windows installers are not Authenticode-signed, and macOS builds use ad-hoc signing without Apple notarization.

The existing `v*` tag flow remains available for signed updater releases. The tag must match the desktop manifests and belong to `main`; configure `TAURI_SIGNING_PRIVATE_KEY` and, only for an encrypted key, `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` in repository Actions secrets. This flow generates `latest.json`, `portable-latest.json` and `update.json`, retaining the existing updater channel and public key. A signing key must match that public key.

Both flows share one build matrix. The automatically scoped `GITHUB_TOKEN` receives `contents: write` for release creation and uploads; reusable CI receives only `contents: read`. Ordinary `main` pushes run CI without publishing a release.

References: [Tauri action inputs](https://github.com/tauri-apps/tauri-action/blob/v1/action.yml), [Tauri action configuration merging](https://github.com/tauri-apps/tauri-action/blob/v1/src/config.ts), [GitHub reusable workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows).
