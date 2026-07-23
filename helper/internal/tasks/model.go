package tasks

// Status describes the lifecycle state of a download task.
type Status string

const (
	Queued      Status = "queued"
	Downloading Status = "downloading"
	Merging     Status = "merging"
	Completed   Status = "completed"
	Failed      Status = "failed"
	Canceled    Status = "canceled"
)

// Task is the public, serializable view of a download attempt.
type Task struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	Status     Status  `json:"status"`
	Progress   float64 `json:"progress"`
	OutputPath string  `json:"outputPath,omitempty"`
	Error      string  `json:"error,omitempty"`
}
