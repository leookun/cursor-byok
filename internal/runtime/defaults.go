package runtime

const (
	// InjectAccountEmail 表示本地模式模拟账号的 email。
	InjectAccountEmail = "cursor@ai.com"
	// InjectAuthToken 表示本地模式模拟账号的 token。
	InjectAuthToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJmYWtlLWN1cnNvci1sb2NhbC11c2VyIiwiZW1haWwiOiJjdXJzb3JAYWkuY29tIiwidHlwZSI6InNlc3Npb24iLCJpc3MiOiJjdXJzb3ItY2xpZW50Iiwic2NvcGUiOiJvcGVuaWQgcHJvZmlsZSBlbWFpbCIsImV4cCI6NDA3MDkwODgwMH0.fake-local-state-token"
	// LocalRelayToken 用于 local 模式下，backend 回源 cursor.sh 时覆盖 Authorization。
	LocalRelayToken = InjectAuthToken
)
