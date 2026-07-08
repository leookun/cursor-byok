package client

import (
	"encoding/json"
	"errors"
)

// LicenseActionRequest 定义了当前模块中的 LicenseActionRequest 类型。
type LicenseActionRequest struct {
	// Host 表示当前声明中的 Host。
	Host string `json:"host"`
	// Code 表示当前声明中的 Code。
	Code string `json:"code"`
	// DeviceID 表示当前声明中的 DeviceID。
	DeviceID string `json:"deviceId"`
	// DeviceMeta 表示当前声明中的 DeviceMeta。
	DeviceMeta string `json:"deviceMeta"`
}

// LicenseSwitchDeviceRequest 定义了当前模块中的 LicenseSwitchDeviceRequest 类型。
type LicenseSwitchDeviceRequest struct {
	// Host 表示当前声明中的 Host。
	Host string `json:"host"`
	// Code 表示当前声明中的 Code。
	Code string `json:"code"`
	// FromDeviceID 表示当前声明中的 FromDeviceID。
	FromDeviceID string `json:"fromDeviceId"`
	// ToDeviceID 表示当前声明中的 ToDeviceID。
	ToDeviceID string `json:"toDeviceId"`
	// Remark 表示当前声明中的 Remark。
	Remark string `json:"remark"`
}

// LicenseAPIResult 定义了当前模块中的 LicenseAPIResult 类型。
type LicenseAPIResult struct {
	// Code 表示当前声明中的 Code。
	Code string `json:"code"`
	// Message 表示当前声明中的 Message。
	Message string `json:"message"`
	// Data 表示当前声明中的 Data。
	Data map[string]any `json:"data,omitempty"`
}

// UsageRecordsRequest 定义了当前模块中的 UsageRecordsRequest 类型。
type UsageRecordsRequest struct {
	// Host 表示当前声明中的 Host。
	Host string `json:"host"`
	// Code 表示当前声明中的 Code。
	Code string `json:"code"`
	// Page 表示当前声明中的 Page。
	Page int `json:"page"`
	// PageSize 表示当前声明中的 PageSize。
	PageSize int `json:"pageSize"`
	// StartTime 表示当前声明中的 StartTime。
	StartTime string `json:"startTime"`
	// EndTime 表示当前声明中的 EndTime。
	EndTime string `json:"endTime"`
	// RequestID 表示当前声明中的 RequestID。
	RequestID string `json:"requestId"`
	// ConversationID 表示当前声明中的 ConversationID。
	ConversationID string `json:"conversationId"`
	// RuntimeModelID 表示当前声明中的 RuntimeModelID。
	RuntimeModelID string `json:"runtimeModelId"`
}

// UsageRecord 定义了当前模块中的 UsageRecord 类型。
type UsageRecord struct {
	// CreatedAt 表示当前声明中的 CreatedAt。
	CreatedAt string `json:"createdAt"`
	// RuntimeModelID 表示当前声明中的 RuntimeModelID。
	RuntimeModelID string `json:"runtimeModelId"`
	// RequestID 表示当前声明中的 RequestID。
	RequestID string `json:"requestId"`
	// ConversationID 表示当前声明中的 ConversationID。
	ConversationID string `json:"conversationId"`
}

// UsageRecordsData 定义了当前模块中的 UsageRecordsData 类型。
type UsageRecordsData struct {
	// Items 表示当前声明中的 Items。
	Items []UsageRecord `json:"items"`
	// Total 表示当前声明中的 Total。
	Total int `json:"total"`
	// Page 表示当前声明中的 Page。
	Page int `json:"page"`
	// PageSize 表示当前声明中的 PageSize。
	PageSize int `json:"pageSize"`
}

// UsageRecordsResult 定义了当前模块中的 UsageRecordsResult 类型。
type UsageRecordsResult struct {
	// Code 表示当前声明中的 Code。
	Code string `json:"code"`
	// Message 表示当前声明中的 Message。
	Message string `json:"message"`
	// Data 表示当前声明中的 Data。
	Data UsageRecordsData `json:"data"`
}

// ActivateLicense 用于处理与 ActivateLicense 相关的逻辑。
func (s *ProxyService) ActivateLicense(LicenseActionRequest) (LicenseAPIResult, error) {
	return LicenseAPIResult{}, errors.New("activation has been removed from the local client")
}

// BindLicenseDevice 用于处理与 BindLicenseDevice 相关的逻辑。
func (s *ProxyService) BindLicenseDevice(LicenseActionRequest) (LicenseAPIResult, error) {
	return LicenseAPIResult{}, errors.New("device binding has been removed from the local client")
}

// SwitchLicenseDevice 用于处理与 SwitchLicenseDevice 相关的逻辑。
func (s *ProxyService) SwitchLicenseDevice(LicenseSwitchDeviceRequest) (LicenseAPIResult, error) {
	return LicenseAPIResult{}, errors.New("device switching has been removed from the local client")
}

// QueryUsageRecords 用于处理与 QueryUsageRecords 相关的逻辑。
func (s *ProxyService) QueryUsageRecords(req UsageRecordsRequest) (UsageRecordsResult, error) {
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	return UsageRecordsResult{
		Code:    "UNSUPPORTED",
		Message: "usage records UI has been removed from the local client",
		Data: UsageRecordsData{
			Items:    []UsageRecord{},
			Total:    0,
			Page:     page,
			PageSize: pageSize,
		},
	}, nil
}

// MarshalJSON 用于处理与 MarshalJSON 相关的逻辑。
func (result LicenseAPIResult) MarshalJSON() ([]byte, error) {
	type alias LicenseAPIResult
	output := alias(result)
	if output.Data == nil {
		output.Data = map[string]any{}
	}
	return json.Marshal(output)
}
