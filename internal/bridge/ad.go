package bridge

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"cursor/internal/ads"

	"github.com/pkg/browser"
)

type AdRuntime = ads.Runtime
type AdWindowConfig = ads.WindowConfig

type AdService struct {
	core *ads.Service
}

func NewAdService(core *ads.Service) *AdService {
	return &AdService{core: core}
}

func (service *AdService) GetAdRuntime() (AdRuntime, error) {
	if service == nil || service.core == nil {
		return AdRuntime{}, fmt.Errorf("广告服务未初始化")
	}
	return service.core.GetRuntime(context.Background())
}

func (service *AdService) OpenExternalURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("仅支持打开 http/https 地址")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return fmt.Errorf("地址缺少主机名")
	}
	return browser.OpenURL(parsed.String())
}
