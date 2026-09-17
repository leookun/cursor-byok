use tauri::{
    menu::{Menu, MenuItem, PredefinedMenuItem},
    tray::TrayIconBuilder,
    App,
};

#[cfg(target_os = "windows")]
use tauri::tray::{MouseButton, MouseButtonState, TrayIconEvent};

use crate::desktop::open_main_window;

const OPEN_MENU_ID: &str = "tray-open";
const QUIT_MENU_ID: &str = "tray-quit";

pub fn create(app: &mut App) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, OPEN_MENU_ID, "打开 Cursor BYOK", true, None::<&str>)?;
    let separator = PredefinedMenuItem::separator(app)?;
    let quit = MenuItem::with_id(app, QUIT_MENU_ID, "退出", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &separator, &quit])?;

    TrayIconBuilder::with_id("main")
        .icon(tauri::include_image!("./icons/32x32.png"))
        .tooltip("Cursor BYOK")
        .menu(&menu)
        .show_menu_on_left_click(cfg!(target_os = "macos"))
        .on_menu_event(|app, event| match event.id().as_ref() {
            OPEN_MENU_ID => {
                let _ = open_main_window(app);
            }
            QUIT_MENU_ID => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            #[cfg(target_os = "windows")]
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                let _ = open_main_window(tray.app_handle());
            }

            #[cfg(not(target_os = "windows"))]
            let _ = (tray, event);
        })
        .build(app)?;
    Ok(())
}
