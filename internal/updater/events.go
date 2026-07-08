package updater

const (
	EventState    = "update:state"
	EventProgress = "update:progress"
	EventReady    = "update:ready"
	EventError    = "update:error"
)

type StatePayload struct {
	State        string  `json:"state"`
	Version      string  `json:"version,omitempty"`
	ReleaseDate  string  `json:"releaseDate,omitempty"`
	ReleaseNotes string  `json:"releaseNotes,omitempty"`
	Downloaded   int64   `json:"downloaded,omitempty"`
	Total        int64   `json:"total,omitempty"`
	Percentage   float64 `json:"percentage,omitempty"`
	Error        string  `json:"error,omitempty"`
	Message      string  `json:"message,omitempty"`
	Prompt       bool    `json:"prompt,omitempty"`
	PromptKind   string  `json:"promptKind,omitempty"`
}

type ProgressPayload struct {
	State      string  `json:"state"`
	Version    string  `json:"version,omitempty"`
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Percentage float64 `json:"percentage"`
}

type ReadyPayload struct {
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	ReleaseDate  string `json:"releaseDate,omitempty"`
	ReleaseNotes string `json:"releaseNotes,omitempty"`
	Prompt       bool   `json:"prompt,omitempty"`
	PromptKind   string `json:"promptKind,omitempty"`
}

type ErrorPayload struct {
	State      string `json:"state"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
	Prompt     bool   `json:"prompt,omitempty"`
	PromptKind string `json:"promptKind,omitempty"`
}
