package models

type Report struct {
	ID       string   `json:"-" gorethink:"id"`
	CommitID string   `json:"commitID" gorethink:"commit_id"`
	Version  string   `json:"version" gorethink:"version"`
	Assets   []string `json:"assets" gorethink:"assets"`
	Entries  []*Entry `json:"entries" gorethink:"entries"`
}

type Entry struct {
	Date          int64         `json:"date" gorethink:"date"`
	OldStacktrace string        `json:"stacktrace" gorethink:"-"`
	NewStacktrace []string      `json:"-" gorethink:"stacktrace"`
	Type          string        `json:"type" gorethink:"type"`
	Message       string        `json:"message" gorethink:"message"`
	Objects       []interface{} `json:"objects" gorethink:"objects"`
}
