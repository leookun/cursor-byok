//! Owns filesystem plugin discovery, sandboxed workers, and plugin providers.
mod asset;
mod builtin;
mod catalog;
mod data;
mod definition;
mod descriptor;
mod installation;
mod manifest;
mod oauth_callback;
mod protocol;
mod registry;
mod runtime;
mod state;
mod wire;
mod worker;

pub use descriptor::{
    parse_model_id, PluginAutomationDescriptor, PluginDescriptor, PluginModelDescriptor,
    PluginProviderDescriptor, PluginResourceDescriptor, PluginResourceView, ADAPTER_ID_PREFIX,
};
pub use registry::{ImportResponse, OAuthBeginResponse, OAuthPollResponse, PluginRegistry};
pub use runtime::{PluginRuntime, PluginRuntimePhase, PluginRuntimeState, PluginRuntimeStatus};

/// Windows 下阻止 Deno 子进程弹出控制台窗口(CREATE_NO_WINDOW)。
#[cfg(windows)]
fn detach_console(command: &mut tokio::process::Command) {
    command.creation_flags(0x0800_0000);
}

#[cfg(not(windows))]
fn detach_console(_command: &mut tokio::process::Command) {}
