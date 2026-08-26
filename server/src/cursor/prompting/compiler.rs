use std::{collections::BTreeMap, path::PathBuf};

use crate::{
    model::{ModelSpec, PromptSpec, ToolDefinition},
    Error, Result,
};

use super::{assets::runtime_expression, Mode, PromptAssets};

#[derive(Clone)]
pub struct PromptCompiler {
    assets: PromptAssets,
    compaction_prompt_path: Option<PathBuf>,
}

impl PromptCompiler {
    pub fn new(assets: PromptAssets) -> Self {
        Self {
            assets,
            compaction_prompt_path: None,
        }
    }

    pub fn with_compaction_prompt_path(assets: PromptAssets, path: PathBuf) -> Self {
        Self {
            assets,
            compaction_prompt_path: Some(path),
        }
    }

    pub fn runtime_message(&self, mode: Mode, values: &BTreeMap<&str, String>) -> Result<String> {
        render(&self.assets.mode(mode).runtime, values)
    }

    pub fn prompt_spec(
        &self,
        mode: Mode,
        model: &ModelSpec,
        dynamic_tools: &[ToolDefinition],
        suppress_subagent_progress: bool,
    ) -> Result<PromptSpec> {
        let mut tools = self.tools(mode, suppress_subagent_progress);
        let mut dynamic_tools = dynamic_tools.to_vec();
        dynamic_tools.sort_by(|left, right| left.name.cmp(&right.name));
        append_dynamic_tools(&mut tools, dynamic_tools)?;
        if !model.supports_image_generation {
            tools.retain(|tool| tool.name != "GenerateImage");
        }
        let fake_model_name = model
            .display_name
            .as_deref()
            .unwrap_or(model.model_id.as_str());
        let prompt = match (mode, &self.compaction_prompt_path) {
            (Mode::Compaction, Some(path)) => crate::config::compaction_prompt_override_at(path)?
                .unwrap_or_else(|| self.assets.mode(mode).prompt.clone()),
            _ => self.assets.mode(mode).prompt.clone(),
        };
        Ok(PromptSpec {
            instructions: prompt.replace("{{FAKE_MODEL_NAME}}", fake_model_name),
            tools,
        })
    }

    fn tools(&self, mode: Mode, suppress_subagent_progress: bool) -> Vec<ToolDefinition> {
        let mut tools = self.assets.mode(mode).tools.clone();
        if mode == Mode::Subagent && suppress_subagent_progress {
            tools.retain(|tool| tool.name != "UpdateCurrentStep");
        }
        tools
    }
}

fn render(template: &str, values: &BTreeMap<&str, String>) -> Result<String> {
    let expression = runtime_expression();
    let mut output = String::with_capacity(template.len());
    let mut cursor = 0;
    for capture in expression.captures_iter(template) {
        let token = capture.get(0).expect("runtime template token");
        let name = &capture[1];
        let value = values
            .get(name)
            .ok_or_else(|| Error::Protocol(format!("runtime template value is missing: {name}")))?;
        output.push_str(&template[cursor..token.start()]);
        output.push_str(value);
        cursor = token.end();
    }
    output.push_str(&template[cursor..]);
    Ok(output.trim().to_string())
}

fn append_dynamic_tools(
    tools: &mut Vec<ToolDefinition>,
    additions: Vec<ToolDefinition>,
) -> Result<()> {
    for tool in additions {
        if tools.iter().any(|existing| existing.name == tool.name) {
            return Err(Error::Protocol(format!(
                "dynamic MCP tool conflicts with a mode tool: {}",
                tool.name
            )));
        }
        tools.push(tool);
    }
    Ok(())
}
