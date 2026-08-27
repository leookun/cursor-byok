use std::{path::Path, sync::OnceLock};

use crate::{model::ToolDefinition, Error, Result};

use super::catalog::Catalog;

static EMBEDDED_PROMPTS: include_dir::Dir<'_> =
    include_dir::include_dir!("$CARGO_MANIFEST_DIR/prompt/cursor");

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Mode {
    Agent,
    Ask,
    Plan,
    Debug,
    Multitask,
    Subagent,
    Compaction,
}

impl Mode {
    pub fn parse(value: &str) -> Result<Self> {
        match value.to_ascii_lowercase().as_str() {
            "agent" => Ok(Self::Agent),
            "ask" => Ok(Self::Ask),
            "plan" => Ok(Self::Plan),
            "debug" => Ok(Self::Debug),
            "multitask" => Ok(Self::Multitask),
            "subagent" => Ok(Self::Subagent),
            "compaction" => Ok(Self::Compaction),
            other => Err(Error::Config(format!("unknown prompt mode: {other}"))),
        }
    }

    fn name(self) -> &'static str {
        match self {
            Self::Agent => "agent",
            Self::Ask => "ask",
            Self::Plan => "plan",
            Self::Debug => "debug",
            Self::Multitask => "multitask",
            Self::Subagent => "subagent",
            Self::Compaction => "compaction",
        }
    }

    fn index(self) -> usize {
        match self {
            Self::Agent => 0,
            Self::Ask => 1,
            Self::Plan => 2,
            Self::Debug => 3,
            Self::Multitask => 4,
            Self::Subagent => 5,
            Self::Compaction => 6,
        }
    }
}

#[derive(Clone, Debug)]
pub struct ModeAssets {
    pub prompt: String,
    pub runtime: String,
    pub tools: Vec<ToolDefinition>,
}

#[derive(Clone, Debug)]
pub struct PromptAssets {
    modes: [ModeAssets; 7],
}

impl PromptAssets {
    pub fn load(root: &Path) -> Result<Self> {
        Self::read(|path| {
            let path = root.join(path);
            path.exists()
                .then(|| std::fs::read_to_string(path).map_err(Error::from))
                .transpose()
        })
    }

    pub fn embedded() -> Result<Self> {
        Self::read(|path| {
            EMBEDDED_PROMPTS
                .get_file(path)
                .map(|file| {
                    file.contents_utf8()
                        .map(str::to_string)
                        .ok_or_else(|| Error::Config(format!("prompt asset is not UTF-8: {path}")))
                })
                .transpose()
        })
    }

    fn read(mut asset: impl FnMut(&str) -> Result<Option<String>>) -> Result<Self> {
        let catalog = Catalog::parse(
            &asset("tools.json")?
                .ok_or_else(|| Error::Config("missing Cursor tools.json".into()))?,
        )?;
        let mut modes = Vec::with_capacity(7);
        for mode in [
            Mode::Agent,
            Mode::Ask,
            Mode::Plan,
            Mode::Debug,
            Mode::Multitask,
            Mode::Subagent,
            Mode::Compaction,
        ] {
            let prompt = asset(&format!("{}/prompt.md", mode.name()))?
                .ok_or_else(|| Error::Config(format!("missing prompt for {mode:?}")))?
                .replace("\r\n", "\n");
            let runtime = asset(&format!("{}/runtime.md", mode.name()))?
                .ok_or_else(|| Error::Config(format!("missing runtime template for {mode:?}")))?
                .replace("\r\n", "\n");
            validate_runtime_template(mode, &runtime)?;
            let manifest = asset(&format!("modes/{}.json", mode.name()))?
                .ok_or_else(|| Error::Config(format!("missing manifest for {mode:?}")))?;
            let tools = catalog.select_json(&manifest)?;
            modes.push(ModeAssets {
                prompt,
                runtime,
                tools,
            });
        }
        Ok(Self {
            modes: modes
                .try_into()
                .map_err(|_| Error::Config("incomplete Cursor prompt mode catalog".into()))?,
        })
    }

    pub fn mode(&self, mode: Mode) -> &ModeAssets {
        &self.modes[mode.index()]
    }
}

const RUNTIME_VARIABLES: &[&str] = &[
    "OPEN_FILES",
    "SELECTED_CONTEXT",
    "ACTION_CONTEXT",
    "TIMESTAMP",
    "USER_QUERY",
    "DEBUG_SERVER_ENDPOINT",
    "DEBUG_LOG_PATH",
    "DEBUG_SESSION_ID",
];

fn validate_runtime_template(mode: Mode, template: &str) -> Result<()> {
    let expression = runtime_expression();
    for capture in expression.captures_iter(template) {
        let name = &capture[1];
        if !RUNTIME_VARIABLES.contains(&name) {
            return Err(Error::Config(format!(
                "unknown variable in {mode:?} runtime template: {name}"
            )));
        }
    }
    for required in ["TIMESTAMP", "USER_QUERY"] {
        let token = format!("{{{{{required}}}}}");
        if !template.contains(&token) {
            return Err(Error::Config(format!(
                "{mode:?} runtime template is missing {token}"
            )));
        }
    }
    let stripped = expression.replace_all(template, "");
    if stripped.contains("{{") || stripped.contains("}}") {
        return Err(Error::Config(format!(
            "malformed placeholder in {mode:?} runtime template"
        )));
    }
    Ok(())
}

pub(super) fn runtime_expression() -> &'static regex::Regex {
    static EXPRESSION: OnceLock<regex::Regex> = OnceLock::new();
    EXPRESSION.get_or_init(|| {
        regex::Regex::new(r"\{\{([A-Z_]+)\}\}").expect("valid runtime placeholder expression")
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn direct_semble_tools_are_available_in_every_working_mode() {
        let assets = PromptAssets::embedded().unwrap();
        for mode in [
            Mode::Agent,
            Mode::Ask,
            Mode::Plan,
            Mode::Debug,
            Mode::Multitask,
            Mode::Subagent,
        ] {
            let names = assets
                .mode(mode)
                .tools
                .iter()
                .map(|tool| tool.name.as_str())
                .collect::<Vec<_>>();
            assert!(names.contains(&"SembleSearch"), "missing in {mode:?}");
            assert!(names.contains(&"SembleFindRelated"), "missing in {mode:?}");
        }
    }
}
