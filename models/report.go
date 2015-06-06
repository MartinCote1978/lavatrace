package models

type Report struct {
	ID       string   `json:"-"`
	CommitID string   `json:"commitID"`
	Version  string   `json:"version"`
	Assets   []string `json:"assets"`
	Entries  []*Entry `json:"entries"`
}

type Entry struct {
	Date       int64         `json:"date"`
	Stacktrace string        `json:"stacktrace"`
	Type       string        `json:"type"`
	Message    string        `json:"message"`
	Objects    []interface{} `json:"objects"`
}
