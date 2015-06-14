package models

type Log struct {
	CommitID string      `json:"commit_id"`
	Version  string      `json:"version"`
	Assets   []string    `json:"assets"`
	Entries  []*LogEntry `json:"entries"`
}

func (l *Log) Class() string { return "sentry_log.Log" }

type LogEntry struct {
	Date    int64       `json:"date"`
	Type    string      `json:"type"`
	Message string      `json:"message"`
	Objects interface{} `json:"objects"`
	Frames  []*LogFrame `json:"frames"`
}

type LogFrame struct {
	Filename    string   `json:"filename"`
	Name        string   `json:"name"`
	LineNo      int      `json:"line_no"`
	ColNo       int      `json:"col_no"`
	InApp       bool     `json:"in_app"`
	ContextPre  []string `json:"context_pre,omitempty"`
	ContextLine string   `json:"context_line,omitempty"`
	ContextPost []string `json:"context_post,omitempty"`
	AbsPath     string   `json:"abs_path"`
	StartLineNo int      `json:"start_line_no"`
}
