use std::{
    process::{Command, ExitCode},
    sync::{
        atomic::{AtomicBool, Ordering},
        Mutex,
    },
    time::Duration,
};

use axum::{
    extract::{Extension, Json},
    http::StatusCode,
    routing::post,
    Router,
};
use tauri::{
    async_runtime::JoinHandle, webview::Color, AppHandle, Manager, RunEvent, WebviewUrl,
    WebviewWindow, WebviewWindowBuilder,
};
use tauri_plugin_opener::OpenerExt;
use tokio_util::sync::CancellationToken;

#[cfg(dev)]
use cursor_server::config::ConsoleSource;
use cursor_server::{App, Config, Result};

#[cfg(not(dev))]
use crate::frontend;
use crate::startup::{self, StartupDiagnostics};
use crate::tray;

pub(crate) const MAIN_WINDOW_LABEL: &str = "main";
const AUTOSTART_ARG: &str = "--autostart";

struct DesktopRuntime {
    shutdown: CancellationToken,
    server: Mutex<Option<JoinHandle<Result<()>>>>,
    exiting: AtomicBool,
    server_addr: std::net::SocketAddr,
}

#[tauri::command]
fn open_terminal_with_command(command: String) -> tauri::Result<()> {
    #[cfg(target_os = "macos")]
    {
        let _ = command;
        Command::new("open").args(["-a", "Terminal"]).status()?;
        Ok(())
    }
    #[cfg(target_os = "windows")]
    {
        Command::new("cmd")
            .args(["/C", "start", "cmd", "/K", &command])
            .spawn()?;
        Ok(())
    }
    #[cfg(target_os = "linux")]
    {
        const TERMINALS: &[(&str, &[&str])] = &[
            ("x-terminal-emulator", &["-e"]),
            ("gnome-terminal", &["--"]),
            ("konsole", &["-e"]),
            ("xfce4-terminal", &["--execute"]),
            ("alacritty", &["-e"]),
            ("kitty", &[]),
        ];
        let script = format!("{command}; exec bash");
        for (terminal, separator) in TERMINALS {
            let mut process = Command::new(terminal);
            process.args(*separator);
            process.arg("bash").arg("-c").arg(&script);
            match process.spawn() {
                Ok(_) => return Ok(()),
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
                Err(error) => return Err(error.into()),
            }
        }
        Err(tauri::Error::from(std::io::Error::new(
            std::io::ErrorKind::NotFound,
            "no supported terminal emulator found",
        )))
    }
}

#[derive(serde::Deserialize)]
struct OpenExternalUrlRequest {
    url: String,
}

fn open_external_url(app: &AppHandle, url: &str) -> std::result::Result<(), String> {
    let parsed = url::Url::parse(url).map_err(|error| format!("invalid URL: {error}"))?;
    if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
        return Err("only absolute HTTP and HTTPS URLs are allowed".into());
    }

    app.opener()
        .open_url(parsed.as_str(), None::<&str>)
        .map_err(|error| format!("failed to open URL: {error}"))
}

async fn open_external_url_handler(
    Extension(app): Extension<AppHandle>,
    Json(request): Json<OpenExternalUrlRequest>,
) -> std::result::Result<StatusCode, (StatusCode, String)> {
    open_external_url(&app, &request.url)
        .map(|_| StatusCode::NO_CONTENT)
        .map_err(|error| (StatusCode::BAD_REQUEST, error))
}

fn desktop_api_router(app: AppHandle) -> Router {
    Router::new()
        .route(
            "/__byok-api__/api/desktop/open-external-url",
            post(open_external_url_handler),
        )
        .layer(Extension(app))
}

fn create_main_window(
    app: &AppHandle,
    address: std::net::SocketAddr,
) -> tauri::Result<WebviewWindow> {
    let url = format!("http://{address}/__byok-api__/")
        .parse()
        .expect("local frontend URL");
    let builder = WebviewWindowBuilder::new(app, MAIN_WINDOW_LABEL, WebviewUrl::External(url))
        .title("Cursor BYOK")
        .inner_size(820.0, 558.0)
        .min_inner_size(820.0, 558.0)
        .center()
        .background_color(Color(20, 20, 20, 255))
        .decorations(cfg!(target_os = "macos"))
        .shadow(true)
        .resizable(true)
        .visible(false);

    #[cfg(target_os = "macos")]
    let builder = builder
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true);

    builder.build()
}

/// 按需打开主窗口:webview 仅在需要界面时创建,关闭窗口即销毁释放内存。
pub(crate) fn open_main_window(app: &AppHandle) -> tauri::Result<()> {
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
        return Ok(());
    }
    let address = app.state::<DesktopRuntime>().server_addr;
    let window = create_main_window(app, address)?;
    window.show()?;
    window.set_focus()?;
    Ok(())
}

pub fn run() -> ExitCode {
    let diagnostics = match StartupDiagnostics::initialize() {
        Ok(diagnostics) => diagnostics,
        Err(error) => {
            startup::report_logging_failure(error.as_ref());
            return ExitCode::FAILURE;
        }
    };
    #[cfg(unix)]
    {
        let open_file_limit = match crate::resource_limits::raise_open_file_limit() {
            Ok(limit) => limit,
            Err(error) => {
                diagnostics.report_fatal(&error);
                return ExitCode::FAILURE;
            }
        };
        tracing::info!(
            requested = crate::resource_limits::REQUESTED_OPEN_FILE_LIMIT,
            previous = open_file_limit.previous,
            effective = open_file_limit.effective,
            hard = open_file_limit.hard,
            "open file limit configured"
        );
    }
    tracing::info!(
        version = env!("CARGO_PKG_VERSION"),
        os = std::env::consts::OS,
        architecture = std::env::consts::ARCH,
        log_directory = %diagnostics.log_directory().display(),
        "desktop starting"
    );

    let started_by_autostart = std::env::args_os().any(|arg| arg == AUTOSTART_ARG);

    let app = tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            open_terminal_with_command,
            crate::update::check_portable_update,
            crate::update::install_portable_update,
        ])
        .plugin(tauri_plugin_single_instance::init(|app, args, _| {
            if !args.iter().any(|arg| arg == AUTOSTART_ARG) {
                let _ = open_main_window(app);
            }
        }))
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_process::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .setup(move |app| {
            app.handle().plugin(tauri_plugin_autostart::init(
                tauri_plugin_autostart::MacosLauncher::LaunchAgent,
                Some(vec![AUTOSTART_ARG]),
            ))?;
            let config = {
                let mut config = Config::desktop()?;
                // 插件的 minAppVersion 按桌面应用版本判定,而不是内嵌 server 库的版本。
                config.app_version = env!("CARGO_PKG_VERSION").into();
                config
            };
            #[cfg(dev)]
            let config = {
                let mut config = config;
                config.console = Some(ConsoleSource::Proxy(
                    "http://127.0.0.1:1420"
                        .parse()
                        .expect("Vite development URL"),
                ));
                config
            };
            let server = tauri::async_runtime::block_on(App::new(config))?
                .merge_router(desktop_api_router(app.handle().clone()));
            #[cfg(not(dev))]
            let server = server.merge_router(frontend::router(app.handle().clone()));
            let listener = tauri::async_runtime::block_on(server.bind())?;
            let address = listener.local_addr()?;
            tauri::async_runtime::block_on(server.harness().cleanup_stale_settings())?;
            let desktop_settings =
                tauri::async_runtime::block_on(server.store().desktop_settings())
                    .unwrap_or_default();
            #[cfg(target_os = "macos")]
            app.handle()
                .set_dock_visibility(desktop_settings.show_dock_icon)?;
            let shutdown = CancellationToken::new();
            let server_shutdown = shutdown.clone();
            let app_handle = app.handle().clone();
            let task = tauri::async_runtime::spawn(async move {
                let result = server.serve_on(listener, server_shutdown).await;
                if let Err(error) = &result {
                    tracing::error!(%error, "desktop server stopped unexpectedly");
                    app_handle.exit(1);
                }
                result
            });
            app.manage(DesktopRuntime {
                shutdown,
                server: Mutex::new(Some(task)),
                exiting: AtomicBool::new(false),
                server_addr: address,
            });
            if desktop_settings.silent_start && started_by_autostart {
                tracing::info!("silent autostart enabled; starting without the main window");
            } else {
                open_main_window(app.handle())?;
            }
            tray::create(app)?;
            crate::update::signal_ready_if_requested()?;
            Ok(())
        })
        .build(tauri::generate_context!());
    let app = match app {
        Ok(app) => app,
        Err(error) => {
            diagnostics.report_fatal(&error);
            return ExitCode::FAILURE;
        }
    };

    app.run(|app, event| match event {
        // code 为 None 表示所有窗口已被关闭(轻量模式),阻止退出,
        // 转发服务继续在托盘后台运行;code 为 Some 时是显式退出请求。
        RunEvent::ExitRequested { code, api, .. } => match code {
            None => api.prevent_exit(),
            Some(_) => {
                let runtime = app.state::<DesktopRuntime>();
                if !runtime.exiting.swap(true, Ordering::AcqRel) {
                    api.prevent_exit();
                    runtime.shutdown.cancel();
                    let server = runtime.server.lock().expect("server lock poisoned").take();
                    let app = app.clone();
                    tauri::async_runtime::spawn(async move {
                        if let Some(server) = server {
                            match tokio::time::timeout(Duration::from_secs(11), server).await {
                                Ok(Ok(Ok(()))) => {}
                                Ok(Ok(Err(error))) => {
                                    tracing::error!(%error, "desktop server shutdown failed")
                                }
                                Ok(Err(error)) => {
                                    tracing::error!(%error, "desktop server task failed")
                                }
                                Err(_) => tracing::warn!("desktop server shutdown timed out"),
                            }
                        }
                        app.exit(0);
                    });
                }
            }
        },
        #[cfg(target_os = "macos")]
        RunEvent::Reopen {
            has_visible_windows: false,
            ..
        } => {
            let _ = open_main_window(app);
        }
        _ => {}
    });

    ExitCode::SUCCESS
}
