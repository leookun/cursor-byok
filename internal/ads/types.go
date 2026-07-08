package ads

const (
	// FetchURL 保留旧名称表示默认广告位地址。
	FetchURL = "https://ads.leokun.cn/1"
	// FetchURL     = "http://localhost:3000/ad.zip"
	RoutePrefix  = "/ad"
	EventUpdated = "ad:updated"
)

type Slot struct {
	ID       string
	FetchURL string
}

var Slots = []Slot{
	{ID: "1", FetchURL: "https://ads.leokun.cn/1"},
	{ID: "2", FetchURL: "https://ads.leokun.cn/2"},
	{ID: "3", FetchURL: "https://ads.leokun.cn/3"},
}

type WindowConfig struct {
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
}

type HomePlacementConfig struct {
	Title    string `json:"title" yaml:"title"`
	Subtitle string `json:"subtitle" yaml:"subtitle"`
}

type Config struct {
	Enabled bool                `json:"enabled" yaml:"enabled"`
	Window  WindowConfig        `json:"window" yaml:"window"`
	Home    HomePlacementConfig `json:"home" yaml:"home"`
}

type Runtime struct {
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
	Slots        []SlotRuntime       `json:"slots"`
}

type SlotRuntime struct {
	ID           string              `json:"id"`
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
}

type MetricsSnapshot struct {
	TurnsTotal         int
	RequestTokensTotal int64
	PromptTokensTotal  int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
}

type FetchResult struct {
	Hash    string
	Changed bool
}
