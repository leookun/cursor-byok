# Local Changes

> Ghi các thay đổi local theo ngày, mới nhất ở đầu file. Mỗi thay đổi dùng tiêu đề `## YYYY-MM-DD` và một mục `###` riêng.

## 2026-08-05

### Changed: Đồng bộ upstream v0.0.45 và dùng checkpoint inline

### Thay đổi

Đã merge `upstream/main` v0.0.45. Cơ chế checkpoint conversation chuyển từ blob-backed turns sang inline turns theo upstream để áp dụng bản vá conversation có thể biến mất.

Các thành phần blob synchronization, imported blobs và checkpoint blob timeout được loại bỏ theo contract mới. Conversation state import và replay hiện giải mã trực tiếp turn/step inline từ checkpoint.

### Behavior fork được giữ lại

- Reasoning chung của một provider pass chỉ được persist ở một tool call; `tool_result` chỉ giữ reasoning fallback khi thiếu `tool_call` tương ứng.
- Bỏ qua partial/delta đến muộn sau khi tool đã completed.
- Hidden `PatchEdit` và `Write` tiếp tục recovery khi transport đóng trước terminal result, đồng thời hoàn tất operation khi turn bị cancel.
- Terminal stream không phát sinh terminal event lặp hoặc cho phép subscribe lại sau khi đã kết thúc.

### Cập nhật từ upstream

- Cập nhật version phát hành lên `0.0.45`.
- Bổ sung tùy chọn tắt WebView sandbox bằng biến môi trường `CURSOR_BYOK_DISABLE_WEBVIEW_SANDBOX` cho môi trường VDI bị ảnh hưởng.

### Xác minh

```powershell
go test ./... -count=1 -timeout 180s
```

Đã chạy thành công. `go vet ./...` vẫn báo hai cảnh báo có sẵn tại `internal/backend/agent/bridge/interaction/bridge.go`, không thuộc thay đổi merge này.

## 2026-08-03

### Fixed: Reasoning/progress bị lặp trước nhiều tool trong cùng một lượt Agent

### Hiện tượng

Sau khi đồng bộ upstream, một reasoning/progress block có thể xuất hiện lặp lại trước mỗi tool trong cùng một lượt Agent. Điều này đặc biệt rõ khi provider trả về nhiều tool call liên tiếp.

### Nguyên nhân

Provider reasoning được tích lũy ở cấp provider pass nhưng trước đây lại được sao chép vào mọi `ToolLikeCompleted`. Cùng dữ liệu reasoning vì thế bị persist nhiều lần trong `tool_call`; khi tool hoàn tất, nó còn bị ghi thêm vào `tool_result`.

Tính năng Cursor transcript sync mới từ upstream render trực tiếp các `tool_call` đã persist, khiến lỗi tiềm ẩn này hiện rõ dưới dạng progress/tool rows lặp trong UI.

### Cách khắc phục

Reasoning buffer hiện có ownership rõ ràng: nó được consume đúng một lần tại tool đầu tiên của provider pass. Nếu provider đồng thời phát text, reasoning được gắn vào `assistant_text` và không sao chép sang tool.

`tool_result` chỉ giữ `reasoning_content` khi thiếu `tool_call` tương ứng, nhằm duy trì khả năng replay cho history cũ hoặc không hoàn chỉnh.

### Xác minh

Đã thêm regression test cho các trường hợp reasoning dùng chung giữa nhiều tool, transcript chỉ render reasoning một lần, và `tool_result` chỉ giữ fallback khi thiếu `tool_call`:

```text
TestTakeProviderOutputForToolConsumesReasoningOnce
TestToolResultReasoningFallbackOnlyPersistsWhenToolCallIsMissing
TestProjectorKeepsSharedReasoningOnOnlyOneOfMultipleToolCalls
TestProjectCursorTranscriptJSONLKeepsSharedReasoningOnOnlyOneTool
```

Lệnh kiểm tra:

```powershell
go test ./internal/backend/forwarder -count=1 -timeout 60s
```

### Fixed: Cursor Agent transcript bị lặp hoặc mất nội dung khi đang trả lời

### Hiện tượng

Khi Agent đang stream câu trả lời hoặc gọi tool, giao diện Cursor có thể hiển thị lặp các dòng tiến trình/file. Một số nội dung vừa hiển thị cũng có thể biến mất hoặc bị thay bằng snapshot cũ hơn.

### Nguyên nhân

Tính năng đồng bộ local conversation history sang Cursor transcript đã ghi lại toàn bộ transcript sau mỗi lần history thay đổi. Việc này xảy ra đồng thời với lúc Cursor client đang append các event stream cho cùng một lượt, dẫn đến hai luồng cùng cập nhật transcript:

- Cursor client render event stream trực tiếp.
- Backend re-project history rồi ghi đè file transcript.

Snapshot history ở thời điểm trung gian có thể chưa chứa toàn bộ event mới nhất, nên gây lặp hoặc làm mất nội dung đang hiển thị.

### Cách khắc phục

Transcript sync hiện được trì hoãn khi lượt Agent còn active (`running`, `waiting_tool`, hoặc `checkpointing`). Backend chỉ ghi transcript sau khi lượt đã kết thúc với một trạng thái terminal như `turn_completed`, `failed`, `provider_error`, hoặc `canceled`.

Snapshot terminal cũng bao gồm `turn_ended`, để transcript hoàn tất khớp với trạng thái cuối của lượt chat.

### Xác minh

Đã thêm regression test kiểm tra transcript không được tạo trong active turn và chỉ được ghi sau `turn_completed`:

```text
TestConversationFileStoreDefersCursorTranscriptSyncUntilTurnEnds
```

Lệnh kiểm tra:

```powershell
go test ./internal/backend/forwarder -run "TestConversationFileStore(DefersCursorTranscriptSyncUntilTurnEnds|SyncsCursorTranscript|BackfillsTranscriptOnStartup)" -count=1 -timeout 30s
```