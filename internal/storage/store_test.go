package storage

import (
	"fmt"
	"os"
	"testing"
)

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Verify root directory was created
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Error("Store directory was not created")
	}
}

func TestAddMessage(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	msg := Message{
		Role:    "user",
		Content: "Hello, world!",
	}

	if err := store.AddMessage(msg); err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	messages, err := store.GetMessages()
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Content != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", messages[0].Content)
	}

	if messages[0].Role != "user" {
		t.Errorf("Expected role 'user', got '%s'", messages[0].Role)
	}
}

func TestMultipleMessages(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	for i := 0; i < 3; i++ {
		msg := Message{
			Role:    "user",
			Content: "Message " + string(rune('A'+i)),
		}
		if err := store.AddMessage(msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	messages, err := store.GetMessages()
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}
}

func TestCompactStart(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Add 10 messages
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d", i),
		}
		if err := store.AddMessage(msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Compact with 500 tokens (~2000 chars) - should keep only recent messages
	if err := store.CompactStart("Test summary", 500); err != nil {
		t.Fatalf("Failed to compact: %v", err)
	}

	// Should now be on session 2
	meta, err := store.GetMetadata()
	if err != nil {
		t.Fatalf("Failed to get metadata: %v", err)
	}

	if meta.SessionNum != 2 {
		t.Errorf("Expected session 2, got %d", meta.SessionNum)
	}

	// Should have summary + 2 kept messages (minimum)
	messages, err := store.GetMessages()
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	// With short messages (~11 chars each), 500 tokens = 2000 chars should fit more than 6
	if len(messages) < 2 {
		t.Errorf("Expected at least 2 messages after compaction, got %d", len(messages))
	}

	// First message should be the summary
	if len(messages) > 0 && messages[0].Role != "system" {
		t.Errorf("Expected first message to be system, got %s", messages[0].Role)
	}
}

func TestCompactPreservesChain(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Add 5 messages
	for i := 0; i < 5; i++ {
		store.AddMessage(Message{Role: "user", Content: fmt.Sprintf("Message %d", i)})
	}

	// Compact
	if err := store.CompactStart("Test summary", 500); err != nil {
		t.Fatalf("Failed to compact: %v", err)
	}

	messages, _ := store.GetMessages()

	// Verify chain: each message's prev should match the previous message's ID
	var prevID string
	for i, msg := range messages {
		if i == 0 {
			// Summary message has no prev (empty or matches nothing)
			continue
		}
		if msg.Prev != prevID {
			t.Errorf("Message %d: expected prev=%q, got %q", i, prevID, msg.Prev)
		}
		if msg.ID == "" {
			t.Errorf("Message %d: ID should not be empty", i)
		}
		prevID = msg.ID
	}
}

func TestCompactKeepsCompletePairs(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Add alternating user/assistant pairs (tool calls would be in assistant messages)
	inputMessages := []Message{
		{Role: "user", Content: "Read file A"},          // 0
		{Role: "assistant", Content: "[tool: read_file]"}, // 1 - tool call
		{Role: "user", Content: "Now read file B"},    // 2
		{Role: "assistant", Content: "[tool: read_file]"}, // 3 - tool call
		{Role: "user", Content: "Continue work"},       // 4
		{Role: "assistant", Content: "Done"},           // 5
	}
	for _, msg := range inputMessages {
		store.AddMessage(msg)
	}

	// Compact with very low token limit - should only keep last pair
	if err := store.CompactStart("Summary", 100); err != nil {
		t.Fatalf("Failed to compact: %v", err)
	}

	kept, _ := store.GetMessages()

	// Should have summary + at most one complete pair (2 messages)
	if len(kept) < 2 {
		t.Fatalf("Expected at least 2 messages (summary + 1 pair), got %d", len(kept))
	}

	// Verify we never have an orphan assistant message (tool call without preceding user)
	for i := 1; i < len(kept); i++ {
		if kept[i].Role == "assistant" {
			// Assistant should always have a user message before it
			foundUser := false
			for j := 1; j < i; j++ {
				if kept[j].Role == "user" {
					foundUser = true
					break
				}
			}
			if !foundUser {
				t.Errorf("Found orphan assistant message at index %d without preceding user", i)
			}
		}
	}
}

func TestMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Add some messages
	for i := 0; i < 5; i++ {
		store.AddMessage(Message{Role: "user", Content: "test"})
	}

	meta, err := store.GetMetadata()
	if err != nil {
		t.Fatalf("Failed to get metadata: %v", err)
	}

	if meta.MessageCount != 5 {
		t.Errorf("Expected message count 5, got %d", meta.MessageCount)
	}

	if meta.SessionNum != 1 {
		t.Errorf("Expected session 1, got %d", meta.SessionNum)
	}
}

func TestEmptyStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	messages, err := store.GetMessages()
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 0 {
		t.Errorf("Expected 0 messages, got %d", len(messages))
	}
}
