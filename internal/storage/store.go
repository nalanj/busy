package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Store handles persistence of agent state using JSONL files
type Store struct {
	dir string
	mu  sync.Mutex
}

// Message represents a stored conversation message
type Message struct {
	ID          string    `json:"id"`
	Prev        string    `json:"prev,omitempty"`
	Role        string    `json:"role"` // user, assistant, system, tool
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	ToolName    string    `json:"tool_name,omitempty"`
	// ToolCallID is the model's tool_use id, used to match a tool result
	// back to the call that produced it. Empty for non-tool rows.
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	// Args is the JSON-encoded arguments of a tool call. Stored alongside
	// the result so the file format round-trips cleanly without flattening
	// the call into the content text (which would put a "$ toolname args"
	// pattern in the model's in-context history, teaching it to write
	// tool calls as text instead of using the function-calling API).
	Args        string    `json:"args,omitempty"`
}

// Metadata stores session metadata
type Metadata struct {
	LastUpdated   time.Time `json:"last_updated"`
	MessageCount int       `json:"message_count"`
	SessionNum   int       `json:"session_num"`
}

// NewStore creates a store in the given directory
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}

	return &Store{
		dir: dir,
	}, nil
}

// Close closes the store (no-op for file-based storage)
func (s *Store) Close() error {
	return nil
}

// sessionPath returns the path for a given session number
func (s *Store) sessionPath(num int) string {
	return filepath.Join(s.dir, fmt.Sprintf("session_%03d.jsonl", num))
}

// metadataPath returns the path to the metadata file
func (s *Store) metadataPath() string {
	return filepath.Join(s.dir, "meta.json")
}

// currentSession returns the current session number
func (s *Store) currentSession() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	re := regexp.MustCompile(`session_(\d+)\.jsonl`)
	maxNum := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := re.FindStringSubmatch(entry.Name())
		if len(matches) >= 2 {
			var num int
			fmt.Sscanf(matches[1], "%d", &num)
			if num > maxNum {
				maxNum = num
			}
		}
	}

	return maxNum, nil
}

// AddMessage adds a message to the current session
func (s *Store) AddMessage(msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get or create current session
	sessionNum, err := s.currentSession()
	if err != nil {
		return err
	}
	if sessionNum == 0 {
		sessionNum = 1
	}

	path := s.sessionPath(sessionNum)

	// Generate ID if not set
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	// Stamp the time if the caller didn't
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	// Find previous message ID
	prevID := s.getLastMessageID(path)
	if prevID != "" {
		msg.Prev = prevID
	}

	// Open file for appending
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	// Write JSON line
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}

	// Update metadata
	s.updateMetadata(sessionNum, 1)

	return nil
}

// UpdateMessage updates an existing message in the session
func (s *Store) UpdateMessage(msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionNum, err := s.currentSession()
	if err != nil || sessionNum == 0 {
		return fmt.Errorf("no session found")
	}

	path := s.sessionPath(sessionNum)

	// Read all messages
	messages, err := s.readSession(path)
	if err != nil {
		return err
	}

	// Find and update the message with matching ID
	for i, m := range messages {
		if m.ID == msg.ID {
			messages[i] = msg
			break
		}
	}

	// Rewrite the session file
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating session file: %w", err)
	}
	defer f.Close()

	for _, m := range messages {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		f.Write(append(data, '\n'))
	}

	return nil
}

// getLastMessageID reads the last message ID from a session file
func (s *Store) getLastMessageID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastID string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		lastID = msg.ID
	}
	return lastID
}

// GetMessages returns all messages from the current session
func (s *Store) GetMessages() ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionNum, err := s.currentSession()
	if err != nil || sessionNum == 0 {
		return []Message{}, nil
	}

	path := s.sessionPath(sessionNum)
	return s.readSession(path)
}

// readSession reads all messages from a session file
func (s *Store) readSession(path string) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Message{}, nil
		}
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	var messages []Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}

	return messages, scanner.Err()
}

// CompactStart begins a new session for compaction
// Keeps complete user/assistant pairs up to retainTokens (estimated ~4 chars per token)
// to avoid splitting tool calls from their results.
func (s *Store) CompactStart(summary string, retainTokens int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current session
	currentNum, err := s.currentSession()
	if err != nil {
		return err
	}
	if currentNum == 0 {
		currentNum = 1
	}

	currentPath := s.sessionPath(currentNum)
	messages, err := s.readSession(currentPath)
	if err != nil {
		return err
	}

	// Calculate how many characters to retain (~4 chars per token)
	retainChars := retainTokens * 4

	// Work backwards keeping complete pairs (user + assistant).
	// Messages alternate: user, assistant, user, assistant...
	// If last is assistant, we need user+assistant together.
	// If last is user, that's a complete pair.
	keepIndex := 0
	accumulatedChars := 0

	for i := len(messages) - 1; i >= 0; i-- {
		msgChars := len(messages[i].Content)

		if messages[i].Role == "user" {
			// Found start of a pair
			pairChars := 0
			for j := i; j < len(messages); j++ {
				pairChars += len(messages[j].Content)
			}

			// Check if adding this pair would exceed budget
			if accumulatedChars+pairChars > retainChars && keepIndex > 0 {
				break
			}

			accumulatedChars += pairChars
			keepIndex = i
		} else if messages[i].Role == "assistant" && keepIndex == 0 {
			// Last message is assistant but no user found yet - include it anyway
			// as long as we have room. This handles the case where the last message
			// is an assistant response (possibly with tool calls).
			pairChars := msgChars
			if i > 0 && messages[i-1].Role == "user" {
				// Include the user message too
				pairChars += len(messages[i-1].Content)
				keepIndex = i - 1
			} else {
				keepIndex = i
			}

			if pairChars <= retainChars {
				accumulatedChars = pairChars
			}
		}
	}

	// Keep at least 2 messages if we have any
	if keepIndex == 0 && len(messages) >= 2 {
		keepIndex = len(messages) - 2
	}

	if keepIndex > 0 {
		messages = messages[keepIndex:]
	}

	// Create new session
	newNum := currentNum + 1
	newPath := s.sessionPath(newNum)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return err
	}

	// Write summary message first
	summaryMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("[Prior conversation summarized: %s]", summary),
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(summaryMsg)
	f, err := os.Create(newPath)
	if err != nil {
		return err
	}
	f.Write(append(data, '\n'))
	f.Close()

	// Write recent messages with reset prev chain
	var lastID string
	for _, msg := range messages {
		msg.Prev = lastID
		msg.Timestamp = time.Now()
		msg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshaling message: %w", err)
		}
		f, err := os.OpenFile(newPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("opening file: %w", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			f.Close()
			return fmt.Errorf("writing message: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("closing file: %w", err)
		}
		lastID = msg.ID
	}

	// Update metadata
	meta, _ := s.getMetadata()
	meta.SessionNum = newNum
	meta.LastUpdated = time.Now()
	meta.MessageCount = len(messages) + 1
	data, _ = json.Marshal(meta)
	return os.WriteFile(s.metadataPath(), data, 0644)
}

// GetMetadata returns session metadata
func (s *Store) GetMetadata() (*Metadata, error) {
	return s.getMetadata()
}

func (s *Store) getMetadata() (*Metadata, error) {
	path := s.metadataPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Metadata{}, nil
		}
		return nil, fmt.Errorf("reading metadata: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshaling metadata: %w", err)
	}

	return &meta, nil
}

func (s *Store) updateMetadata(sessionNum, messageDelta int) {
	meta, err := s.getMetadata()
	if err != nil {
		meta = &Metadata{}
	}
	meta.LastUpdated = time.Now()
	if sessionNum > meta.SessionNum {
		meta.SessionNum = sessionNum
	}
	meta.MessageCount += messageDelta

	data, _ := json.Marshal(meta)
	os.WriteFile(s.metadataPath(), data, 0644)
}
