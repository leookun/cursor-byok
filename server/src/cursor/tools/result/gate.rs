use crate::{cursor::proto::agent::v1 as pb, model::truncate_edges};

const KIB: usize = 1024;
const SHELL_STREAM_LIMIT: usize = 16 * KIB;
const SHELL_CONTENT_LIMIT: usize = 32 * KIB;

pub(super) fn model_content(tool: &pb::tool_call::Tool, content: &mut String) {
    if matches!(tool, pb::tool_call::Tool::ShellToolCall(_)) {
        *content = truncate_edges("Shell", content, SHELL_CONTENT_LIMIT);
    }
}

pub(super) fn exec_message(message: &mut pb::exec_client_message::Message) {
    use pb::exec_client_message::Message;
    match message {
        Message::ShellResult(result) | Message::MiniSweAgentBashResult(result) => {
            gate_shell_result(result)
        }
        _ => {}
    }
}

fn gate_shell_result(result: &mut pb::ShellResult) {
    use pb::shell_result::Result;
    match result.result.as_mut() {
        Some(Result::Success(success)) => {
            success.stdout = truncate_edges("Shell stdout", &success.stdout, SHELL_STREAM_LIMIT);
            success.stderr = truncate_edges("Shell stderr", &success.stderr, SHELL_STREAM_LIMIT);
            if let Some(interleaved) = success.interleaved_output.as_mut() {
                *interleaved =
                    truncate_edges("Shell interleaved output", interleaved, SHELL_CONTENT_LIMIT);
            }
        }
        Some(Result::Failure(failure)) => {
            failure.stdout = truncate_edges("Shell stdout", &failure.stdout, SHELL_STREAM_LIMIT);
            failure.stderr = truncate_edges("Shell stderr", &failure.stderr, SHELL_STREAM_LIMIT);
            if let Some(interleaved) = failure.interleaved_output.as_mut() {
                *interleaved =
                    truncate_edges("Shell interleaved output", interleaved, SHELL_CONTENT_LIMIT);
            }
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn shell_tool() -> pb::tool_call::Tool {
        pb::tool_call::Tool::ShellToolCall(pb::ShellToolCall::default())
    }

    #[test]
    fn shell_output_keeps_both_ends_within_its_budget() {
        let mut content = format!("HEAD{}TAIL", " ".repeat(1024 * KIB));

        model_content(&shell_tool(), &mut content);

        assert!(content.len() <= SHELL_CONTENT_LIMIT);
        assert!(content.starts_with("HEAD"));
        assert!(content.ends_with("TAIL"));
        assert!(content.contains("omitted middle"));
    }

    #[test]
    fn non_shell_output_is_unchanged() {
        let mut content = "x".repeat(64 * KIB);
        let original = content.clone();

        model_content(
            &pb::tool_call::Tool::ReadToolCall(pb::ReadToolCall::default()),
            &mut content,
        );

        assert_eq!(content, original);
    }

    #[test]
    fn shell_streams_are_limited_before_rendering() {
        let mut message = pb::exec_client_message::Message::ShellResult(pb::ShellResult {
            result: Some(pb::shell_result::Result::Success(pb::ShellSuccess {
                stdout: format!("HEAD{}TAIL", "x".repeat(64 * KIB)),
                stderr: format!("ERROR_HEAD{}ERROR_TAIL", "y".repeat(64 * KIB)),
                interleaved_output: Some(format!("START{}END", "z".repeat(64 * KIB))),
                ..Default::default()
            })),
            ..Default::default()
        });

        exec_message(&mut message);

        let pb::exec_client_message::Message::ShellResult(result) = message else {
            panic!("expected Shell result");
        };
        let Some(pb::shell_result::Result::Success(success)) = result.result else {
            panic!("expected Shell success");
        };
        assert!(success.stdout.len() <= SHELL_STREAM_LIMIT);
        assert!(success.stdout.starts_with("HEAD"));
        assert!(success.stdout.ends_with("TAIL"));
        assert!(success.stderr.len() <= SHELL_STREAM_LIMIT);
        assert!(success.stderr.starts_with("ERROR_HEAD"));
        assert!(success.stderr.ends_with("ERROR_TAIL"));
        assert!(success.interleaved_output.unwrap().len() <= SHELL_CONTENT_LIMIT);
    }

    #[test]
    fn failed_shell_streams_are_limited() {
        let mut message = pb::exec_client_message::Message::ShellResult(pb::ShellResult {
            result: Some(pb::shell_result::Result::Failure(pb::ShellFailure {
                stdout: "x".repeat(64 * KIB),
                stderr: "y".repeat(64 * KIB),
                interleaved_output: Some("z".repeat(64 * KIB)),
                ..Default::default()
            })),
            ..Default::default()
        });

        exec_message(&mut message);

        let pb::exec_client_message::Message::ShellResult(result) = message else {
            panic!("expected Shell result");
        };
        let Some(pb::shell_result::Result::Failure(failure)) = result.result else {
            panic!("expected Shell failure");
        };
        assert!(failure.stdout.len() <= SHELL_STREAM_LIMIT);
        assert!(failure.stderr.len() <= SHELL_STREAM_LIMIT);
        assert!(failure.interleaved_output.unwrap().len() <= SHELL_CONTENT_LIMIT);
    }
}
