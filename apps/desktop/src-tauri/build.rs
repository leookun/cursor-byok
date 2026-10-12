fn main() {
    let manifest = tauri_build::AppManifest::new().commands(&[
        "pick_plugin_directory",
        "open_terminal_with_command",
        "check_portable_update",
        "install_portable_update",
    ]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("failed to build Tauri application")
}
