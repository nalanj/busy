package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/nalanj/aadc/internal/queue"
	"github.com/nalanj/aadc/internal/storage"
)

type Server struct {
	addr          string
	store         *storage.Store
	queue         *queue.Queue
	agentName     string
	sseHub        *SSEHub
	webMsgJobs    []queue.WebMessageJob // jobs that respond to web messages
	webMsgEmitter func(job queue.Job)   // callback to enqueue a web message job
}

func New(addr string, store *storage.Store, queue *queue.Queue, agentName string) *Server {
	return &Server{
		addr:      addr,
		store:     store,
		queue:     queue,
		agentName: agentName,
		sseHub:    NewSSEHub(),
	}
}

// SetWebMessageJobs configures the server to handle web-message jobs
func (s *Server) SetWebMessageJobs(jobs []queue.WebMessageJob, emitter func(queue.Job)) {
	s.webMsgJobs = jobs
	s.webMsgEmitter = emitter
}

// SSEHub returns the SSE hub for publishing events
func (s *Server) SSEHub() *SSEHub {
	return s.sseHub
}

func (s *Server) Start() error {
	http.HandleFunc("/", s.handleIndex)
	http.HandleFunc("/events", s.handleEvents)
	http.HandleFunc("/web-message", s.handleWebMessage)
	return http.ListenAndServe(s.addr, nil)
}

func (s *Server) handleWebMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if len(s.webMsgJobs) == 0 {
		http.Error(w, "No web-message handler configured", http.StatusNotFound)
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Use the first web-message job
	job := s.webMsgJobs[0].ProcessWebMessage(req.Message)

	// Emit queue event
	if s.sseHub != nil {
		s.sseHub.Emit("queue", map[string]interface{}{"action": "enqueue", "job": job})
	}

	// Call the emitter to enqueue the job
	if s.webMsgEmitter != nil {
		s.webMsgEmitter(job)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to events
	ch, cleanup := s.sseHub.Subscribe()
	defer cleanup()

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Stream events
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	meta, _ := s.store.GetMetadata()
	messages, _ := s.store.GetMessages()
	queueLen, _ := s.queue.Len()
	queuedJobs, _ := s.getQueuedJobs()

	hasWebMsg := len(s.webMsgJobs) > 0
	html := s.renderHTML(meta, messages, queueLen, queuedJobs, hasWebMsg)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func (s *Server) getQueuedJobs() ([]queue.Job, error) {
	jobs := []queue.Job{}
	for {
		job, err := s.queue.Dequeue()
		if err != nil {
			return jobs, err
		}
		if job == nil {
			break
		}
		jobs = append(jobs, *job)
	}
	// Re-enqueue all jobs
	for _, job := range jobs {
		s.queue.Enqueue(job)
	}
	return jobs, nil
}

func (s *Server) renderHTML(meta *storage.Metadata, messages []storage.Message, queueLen int, queuedJobs []queue.Job, hasWebMsg bool) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
<title>AADC - ` + html.EscapeString(s.agentName) + `</title>
<style>
body { font-family: monospace; margin: 20px; background: #1a1a2e; color: #eee; }
h1 { color: #00d9ff; }
h1 span { font-size: 0.5em; }
h2 { color: #ff6b6b; margin-top: 30px; border-bottom: 1px solid #333; padding-bottom: 5px; }
.meta { color: #888; margin-bottom: 20px; }
.meta span { margin-right: 20px; }
.message { margin: 10px 0; padding: 10px; border-radius: 5px; }
.user { background: #2d4a3e; border-left: 3px solid #4caf50; }
.assistant { background: #2d3a4a; border-left: 3px solid #2196f3; }
.system { background: #3a2d4a; border-left: 3px solid #9c27b0; color: #b388ff; }
.tool { background: #3a3a2a; border-left: 3px solid #ff9800; font-size: 0.9em; }
.role { font-weight: bold; margin-bottom: 5px; }
.timestamp { color: #666; font-size: 0.8em; }
.content { white-space: pre-wrap; word-break: break-word; }
.content.truncated { max-height: 200px; overflow: hidden; }
.tool-call { color: #ff9800; }
.tool-result { color: #888; font-size: 0.9em; }
.compaction { background: #4a2d4a; border: 1px dashed #9c27b0; padding: 10px; margin: 10px 0; }
#toggle { background: #333; color: #fff; border: none; padding: 5px 10px; cursor: pointer; }
.live-message { animation: fadeIn 0.3s ease-in; }
@keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }
</style>
</head>
<body>
<h1>` + html.EscapeString(s.agentName) + ` <span id="sse-status">● LIVE</span></h1>
<div class="meta">
<span>Session: ` + fmt.Sprintf("%d", meta.SessionNum) + `</span>
<span>Messages: ` + fmt.Sprintf("%d", meta.MessageCount) + `</span>
<span>Last Updated: ` + formatTime(meta.LastUpdated) + `</span>
</div>
<h2>Queue (` + fmt.Sprintf("%d", queueLen) + `)</h2>
<div class="queue" id="queue-container">
`)

	// Render queued jobs
	if len(queuedJobs) == 0 {
		sb.WriteString("<p>Queue is empty</p>")
	} else {
		for i, job := range queuedJobs {
			sb.WriteString(fmt.Sprintf(`<div class="message assistant" id="queue-%d">
<div class="role">QUEUED: %s <span class="timestamp">%s</span></div>
<div class="content">%s</div>
</div>`,
				i, html.EscapeString(job.Name), formatTime(job.EnqueuedAt), html.EscapeString(job.Prompt)))
		}
	}

	sb.WriteString(`</div>
<h2>Messages</h2>
<div id="messages-container">
`)

	// Render messages
	for i, msg := range messages {
		cssClass := msg.Role
		roleLabel := strings.ToUpper(msg.Role)

		// Check if this is a compaction summary
		isCompaction := strings.HasPrefix(msg.Content, "[Prior conversation summarized:")

		if isCompaction {
			sb.WriteString(fmt.Sprintf(`<div class="message compaction">
<div class="role">🔄 COMPACTION</div>
<div class="content">%s</div>
</div>`, html.EscapeString(msg.Content)))
			continue
		}

		// Detect tool calls vs regular content
		content := html.EscapeString(msg.Content)
		if strings.Contains(content, "[tool:") {
			sb.WriteString(fmt.Sprintf(`<div class="message %s" id="msg-%d">
<div class="role">%s <span class="timestamp">%s</span></div>
<div class="tool-call">%s</div>
</div>`, cssClass, i, roleLabel, formatTime(msg.Timestamp), content))
		} else {
			sb.WriteString(fmt.Sprintf(`<div class="message %s" id="msg-%d">
<div class="role">%s <span class="timestamp">%s</span></div>
<div class="content">%s</div>
</div>`, cssClass, i, roleLabel, formatTime(msg.Timestamp), content))
		}
	}

	sb.WriteString(`</div>
<script>
const statusEl = document.getElementById('sse-status');
const messagesContainer = document.getElementById('messages-container');

function connectSSE() {
    const events = new EventSource('/events');
    
    events.addEventListener('connected', () => {
        statusEl.textContent = '● LIVE';
        statusEl.style.color = '#4caf50';
    });

    events.addEventListener('tool', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message tool live-message';
        const toolName = data.content && data.content.tool || '';
        const toolInput = data.content && data.content.content || '';
        msg.innerHTML = '<div class="role">🔧 ' + escapeHtml(toolName) + ' <span class="timestamp">' + new Date().toLocaleTimeString() + '</span></div><div class="tool-call">' + escapeHtml(toolInput) + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('tool_result', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message tool live-message';
        const result = data.content && data.content.content || '';
        msg.innerHTML = '<div class="tool-result">↳ ' + escapeHtml(result) + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('user', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message user live-message';
        const trigger = data.content && data.content.trigger || '';
        const triggerLabel = trigger ? ' [' + trigger + ']' : '';
        msg.innerHTML = '<div class="role">📩 USER' + triggerLabel + ' <span class="timestamp">' + new Date().toLocaleTimeString() + '</span></div><div class="content">' + escapeHtml(data.content && data.content.content || '') + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('agent', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message assistant live-message';
        msg.innerHTML = '<div class="role">🤖 AGENT <span class="timestamp">' + new Date().toLocaleTimeString() + '</span></div><div class="content">' + escapeHtml(data.content && data.content.content || data.content || '') + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('compaction', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message compaction live-message';
        msg.innerHTML = '<div class="role">🔄 COMPACTION</div><div class="content">' + escapeHtml(data.content && data.content.content || data.content || '') + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('done', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message system live-message';
        msg.innerHTML = '<div class="role">✅ DONE</div><div class="content">' + escapeHtml(data.content && data.content.message || data.message || '') + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('error', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'message system live-message';
        msg.innerHTML = '<div class="role">❌ ERROR</div><div class="content">' + escapeHtml(data.content && data.content.message || data.message || '') + '</div>';
        messagesContainer.appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('queue', (e) => {
        const data = JSON.parse(e.data);
        const action = data.action;
        const job = data.job;
        if (action === 'enqueue') {
            const queueEl = document.getElementById('queue-container');
            const emptyMsg = queueEl.querySelector('p');
            if (emptyMsg) emptyMsg.remove();
            const queueItem = document.createElement('div');
            queueItem.className = 'message assistant live-message';
            queueItem.innerHTML = '<div class="role">QUEUED: ' + escapeHtml(job.name) + ' <span class="timestamp">' + new Date().toLocaleTimeString() + '</span></div><div class="content">' + escapeHtml(job.prompt) + '</div>';
            queueEl.appendChild(queueItem);
            queueItem.scrollIntoView({ behavior: 'smooth' });
        } else if (action === 'dequeue') {
            const queueItems = document.querySelectorAll('#queue-container .message');
            for (const item of queueItems) {
                if (item.textContent.includes(job.name)) {
                    item.remove();
                    break;
                }
            }
        }
    });

    events.onerror = () => {
        statusEl.textContent = '✗ RECONNECTING...';
        statusEl.style.color = '#f44336';
        setTimeout(connectSSE, 3000);
    };
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

connectSSE();
</script>
`)

	// Add web message form if configured
	if hasWebMsg {
		sb.WriteString(`
<style>
#web-msg-form { margin-top: 20px; background: #252540; padding: 15px; border-radius: 8px; border: 1px solid #333; }
#web-msg-form textarea { width: 100%; background: #1a1a2e; border: 1px solid #333; color: #eee; padding: 10px; font-family: monospace; border-radius: 4px; resize: vertical; min-height: 80px; box-sizing: border-box; }
#web-msg-form button { background: #00d9ff; color: #1a1a2e; border: none; padding: 10px 20px; cursor: pointer; font-weight: bold; border-radius: 4px; margin-top: 10px; }
#web-msg-form button:disabled { background: #666; cursor: not-allowed; }
</style>
<form id="web-msg-form">
    <textarea id="web-msg-input" placeholder="Type a message..." rows="4"></textarea>
    <button type="submit" id="web-msg-submit">Send</button>
</form>
<script>
const webMsgForm = document.getElementById('web-msg-form');
const webMsgInput = document.getElementById('web-msg-input');
const webMsgSubmit = document.getElementById('web-msg-submit');

webMsgForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const message = webMsgInput.value.trim();
    if (!message) return;
    
    webMsgSubmit.disabled = true;
    webMsgInput.disabled = true;
    
    try {
        const resp = await fetch('/web-message', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ message })
        });
        if (resp.ok) {
            webMsgInput.value = '';
        }
    } finally {
        webMsgSubmit.disabled = false;
        webMsgInput.disabled = false;
        webMsgInput.focus();
    }
});

webMsgInput.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        webMsgForm.dispatchEvent(new Event('submit'));
    }
});
</script>
`)
	}

	sb.WriteString(`
</body>
</html>
`)

	return sb.String()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("2006-01-02 15:04:05")
}
