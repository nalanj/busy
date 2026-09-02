package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"
)

// Job represents a queued job
type Job struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prompt     string    `json:"prompt"`
	Trigger    string    `json:"trigger"` // what triggered this job: @session-start, @web-message, cron expression, etc.
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// WebMessageJob represents a job triggered by web messages
type WebMessageJob struct {
	Name   string
	Prompt string // supports text/template with {{.Message}}
}

// WebMessageData is the data passed to the template
type WebMessageData struct {
	Message string
}

// ProcessWebMessage applies template to the prompt and returns the processed job
func (w *WebMessageJob) ProcessWebMessage(message string) Job {
	tmpl, err := template.New("prompt").Parse(w.Prompt)
	if err != nil {
		// If template parsing fails, use raw prompt
		return Job{
			ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
			Name:       w.Name,
			Prompt:     w.Prompt + "\n\n" + message,
			Trigger:    "@web-message",
			EnqueuedAt: time.Now(),
		}
	}

	var buf bytes.Buffer
	data := WebMessageData{Message: message}
	if err := tmpl.Execute(&buf, data); err != nil {
		return Job{
			ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
			Name:       w.Name,
			Prompt:     w.Prompt + "\n\n" + message,
			Trigger:    "@web-message",
			EnqueuedAt: time.Now(),
		}
	}

	return Job{
		ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:       w.Name,
		Prompt:     buf.String(),
		Trigger:    "@web-message",
		EnqueuedAt: time.Now(),
	}
}

// Queue is a simple on-disk FIFO queue using JSONL
type Queue struct {
	dir       string
	queuePath string
	mu        sync.Mutex
}

// New creates a new queue in the given directory
func New(dir, agentName string) (*Queue, error) {
	queueDir := filepath.Join(dir, agentName)
	if err := os.MkdirAll(queueDir, 0755); err != nil {
		return nil, fmt.Errorf("creating queue dir: %w", err)
	}

	return &Queue{
		dir:       queueDir,
		queuePath: filepath.Join(queueDir, "job_queue.jsonl"),
	}, nil
}

// Enqueue adds a job to the queue
func (q *Queue) Enqueue(job Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Generate ID
	job.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	job.EnqueuedAt = time.Now()

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}

	f, err := os.OpenFile(q.queuePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening queue file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing job: %w", err)
	}

	return nil
}

// Dequeue removes and returns the next job from the queue
// Returns nil if queue is empty
func (q *Queue) Dequeue() (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Read all jobs
	jobs, err := q.readAll()
	if err != nil {
		return nil, err
	}

	if len(jobs) == 0 {
		return nil, nil
	}

	// Take the first job
	job := jobs[0]
	remaining := jobs[1:]

	// Rewrite queue without the first job
	if err := q.writeAll(remaining); err != nil {
		return nil, err
	}

	return &job, nil
}

// Peek returns the next job without removing it
func (q *Queue) Peek() (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	jobs, err := q.readAll()
	if err != nil {
		return nil, err
	}

	if len(jobs) == 0 {
		return nil, nil
	}

	return &jobs[0], nil
}

// Len returns the number of jobs in the queue
func (q *Queue) Len() (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	jobs, err := q.readAll()
	if err != nil {
		return 0, err
	}

	return len(jobs), nil
}

// readAll reads all jobs from the queue file
func (q *Queue) readAll() ([]Job, error) {
	f, err := os.Open(q.queuePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Job{}, nil
		}
		return nil, fmt.Errorf("opening queue file: %w", err)
	}
	defer f.Close()

	var jobs []Job
	decoder := json.NewDecoder(f)
	for decoder.More() {
		var job Job
		if err := decoder.Decode(&job); err != nil {
			continue // Skip malformed lines
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// writeAll writes all jobs to the queue file
func (q *Queue) writeAll(jobs []Job) error {
	if len(jobs) == 0 {
		// Remove the queue file if empty
		os.Remove(q.queuePath)
		return nil
	}

	f, err := os.Create(q.queuePath)
	if err != nil {
		return fmt.Errorf("creating queue file: %w", err)
	}
	defer f.Close()

	for _, job := range jobs {
		data, err := json.Marshal(job)
		if err != nil {
			continue
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("writing job: %w", err)
		}
	}

	return nil
}
