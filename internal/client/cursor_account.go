package client

import (
	"context"
	"fmt"
	"time"

	"cursor/internal/cursoraccount"
)

type CursorAccountStatus = cursoraccount.Status

func (s *ProxyService) GetCursorAccountStatus() CursorAccountStatus {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateSignedOut}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.cursorAccount.EnsureEmail(ctx)
	return s.cursorAccount.Status()
}

func (s *ProxyService) StartCursorAccountLogin() (CursorAccountStatus, error) {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateError}, fmt.Errorf("Cursor 账号服务未初始化")
	}
	return s.cursorAccount.StartLogin()
}

func (s *ProxyService) DisconnectCursorAccount() (CursorAccountStatus, error) {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateSignedOut}, nil
	}
	return s.cursorAccount.Disconnect()
}
