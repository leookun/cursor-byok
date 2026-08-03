# Local Changes

> Ghi các thay đổi local theo ngày, mới nhất ở đầu file. Mỗi thay đổi dùng tiêu đề `## YYYY-MM-DD` và một mục `###` riêng.

## 2026-08-03

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