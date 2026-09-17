mod desktop;
#[cfg(not(dev))]
mod frontend;
mod resource_limits;
mod startup;
mod tray;
mod update;

// mimalloc 在释放时主动向操作系统归还内存,避免 glibc 保留页导致
// 关闭窗口后 RSS 无法回落到静默启动水平。
#[global_allocator]
static GLOBAL_ALLOCATOR: mimalloc::MiMalloc = mimalloc::MiMalloc;

pub fn run() -> std::process::ExitCode {
    if let Some(exit_code) = update::run_replacement_if_requested() {
        return exit_code;
    }
    desktop::run()
}
