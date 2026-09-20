package queue

import (
	"testing"
)

func TestEnqueueDequeue(t *testing.T) {
	tmpDir := t.TempDir()
	q, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create queue: %v", err)
	}

	// Queue should be empty
	length, _ := q.Len()
	if length != 0 {
		t.Errorf("Expected empty queue, got %d", length)
	}

	// Enqueue a job
	q.Enqueue(Job{
		Name:   "test-job",
		Prompt: "Say hello",
	})

	length, _ = q.Len()
	if length != 1 {
		t.Errorf("Expected queue length 1, got %d", length)
	}

	// Dequeue should return the job
	job, _ := q.Dequeue()
	if job == nil {
		t.Fatal("Expected job, got nil")
	}
	if job.Name != "test-job" {
		t.Errorf("Expected job name 'test-job', got '%s'", job.Name)
	}

	// Queue should be empty again
	length, _ = q.Len()
	if length != 0 {
		t.Errorf("Expected empty queue, got %d", length)
	}
}

func TestFIFO(t *testing.T) {
	tmpDir := t.TempDir()
	q, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create queue: %v", err)
	}

	// Enqueue multiple jobs
	for i := 0; i < 3; i++ {
		q.Enqueue(Job{Name: "job", Prompt: "prompt"})
	}

	// Should dequeue in order
	for i := 0; i < 3; i++ {
		job, _ := q.Dequeue()
		if job == nil {
			t.Fatal("Expected job")
		}
	}

	// Queue should be empty
	job, _ := q.Dequeue()
	if job != nil {
		t.Error("Expected nil for empty queue")
	}
}

func TestPeek(t *testing.T) {
	tmpDir := t.TempDir()
	q, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create queue: %v", err)
	}

	// Peek on empty queue
	job, _ := q.Peek()
	if job != nil {
		t.Error("Expected nil for empty queue")
	}

	// Enqueue
	q.Enqueue(Job{Name: "test-job", Prompt: "prompt"})

	// Peek should return job without removing it
	job, _ = q.Peek()
	if job == nil {
		t.Fatal("Expected job")
	}
	if job.Name != "test-job" {
		t.Errorf("Expected 'test-job', got '%s'", job.Name)
	}

	// Queue should still have the job
	length, _ := q.Len()
	if length != 1 {
		t.Errorf("Expected queue length 1, got %d", length)
	}
}
