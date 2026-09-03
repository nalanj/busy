package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/nalanj/aadc/internal/queue"
	"github.com/nalanj/aadc/internal/storage"
)

//go:embed style.css
var staticFS embed.FS

type Server struct {
	addr          string
	store         *storage.Store
	queue         *queue.Queue
	agentName     string
	modelName     string
	systemPrompt  string
	doneToken     string
	workspace     string
	containerID   string
	sseHub        *SSEHub
	webMsgJobs    []queue.WebMessageJob
	webMsgEmitter func(job queue.Job)
}

func New(addr string, store *storage.Store, q *queue.Queue, agentName string) *Server {
	return &Server{
		addr:      addr,
		store:     store,
		queue:     q,
		agentName: agentName,
		sseHub:    NewSSEHub(),
	}
}

func (s *Server) SetSystemPrompt(prompt string) {
	s.systemPrompt = prompt
}

func (s *Server) SetModelName(name string) {
	s.modelName = name
}

func (s *Server) SetDoneToken(token string) {
	s.doneToken = token
}

func (s *Server) SetWorkspace(path string) {
	s.workspace = path
}

func (s *Server) SetContainerID(id string) {
	s.containerID = id
}

func (s *Server) SetWebMessageJobs(jobs []queue.WebMessageJob, emitter func(queue.Job)) {
	s.webMsgJobs = jobs
	s.webMsgEmitter = emitter
}

func (s *Server) SSEHub() *SSEHub {
	return s.sseHub
}

func (s *Server) Start() error {
	http.HandleFunc("/style.css", s.handleStyle)
	http.HandleFunc("/", s.handleIndex)
	http.HandleFunc("/events", s.handleEvents)
	http.HandleFunc("/web-message", s.handleWebMessage)
	return http.ListenAndServe(s.addr, nil)
}

func (s *Server) handleStyle(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("style.css")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css")
	w.Write(data)
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

	job := s.webMsgJobs[0].ProcessWebMessage(req.Message)

	if s.sseHub != nil {
		s.sseHub.Emit("queue", map[string]interface{}{"action": "enqueue", "job": job})
	}

	if s.webMsgEmitter != nil {
		s.webMsgEmitter(job)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	ch, cleanup := s.sseHub.Subscribe()
	defer cleanup()

	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

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

	hasWebMsg := len(s.webMsgJobs) > 0
	html := s.renderHTML(meta, messages, queueLen, hasWebMsg)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func (s *Server) renderHTML(meta *storage.Metadata, messages []storage.Message, queueLen int, hasWebMsg bool) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
<title>` + html.EscapeString(s.agentName) + `</title>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@100..900&family=JetBrains+Mono:wght@100..800&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/style.css">
</head>
<body>
<div class="app">
<div class="main">
<div class="header">
<div class="brand">
<div class="logo">
<svg viewBox="0 0 14 14" fill="#22D3EE"><path d="M4.522 1.764q-.14.041-.267.167-.167.154-.174.373-.007.215.126.386.133.167.355.222.099.014.981.014l.882 0 0 1.148-1.429 0q-1.104 0-1.449.014-.342.014-.496.055-.434.126-.749.42-.314.294-.455.714-.072.198-.085.4-.014.202-.014.96l0 .937-.349 0q-.267.014-.332.021-.062.007-.133.048-.236.099-.321.338-.082.236.027.461.044.068.12.14.079.068.154.106.078.034.14.041.065.007.332.021l.362 0 0 1.791q.014.167.027.294.099.448.379.786.28.335.701.502.154.058.308.099.126.014.643.027l3.192 0 3.192 0q.516-.014.643-.041.533-.099.902-.468.373-.373.485-.906.014-.126.027-.294l0-1.791.362 0q.267-.014.328-.021.065-.007.14-.041.079-.038.154-.106.079-.072.113-.14.038-.072.051-.181.041-.198-.051-.366-.089-.167-.284-.266-.072-.027-.137-.034-.062-.007-.328-.021l-.349 0 0-.937q0-.759-.014-.96-.014-.202-.085-.4-.14-.42-.455-.714-.314-.294-.749-.42-.154-.041-.499-.055-.342-.014-1.446-.014l-1.429 0 0-.923q0-.673 0-.813 0-.14-.027-.209-.085-.212-.294-.325-.326l-.099-.041-1.289-.014q-1.275 0-1.343.014zm6.231 3.541q.209.113.294.325.027.068.027.39l0 4.648-.055.085q-.099.181-.267.28l-.085.041-7.335 0-.085-.041-.267-.28-.055-.085 0-4.635q0-.335.027-.403.058-.126.167-.232.113-.106.239-.133.085-.014 3.681-.014l3.613.014.099.041z"/></svg>
</div>
<span class="app-name">` + html.EscapeString(s.agentName) + `</span>
<div class="live-badge"><div class="live-dot" id="sse-dot"></div><span class="live-text" id="sse-text">LIVE</span></div>
</div>
<div class="tabs">
<button class="tab active" data-tab="log">Log</button>
<button class="tab" data-tab="queue">Queue <span class="tab-badge" id="queue-badge">` + fmt.Sprintf("%d", queueLen) + `</span></button>
<button class="tab" data-tab="config">Config</button>
</div>
</div>
<div class="content">
`)

	// Log tab
	sb.WriteString(`<div class="tab-panel active" id="panel-log">
<div class="feed-header">
<div class="feed-filters">
<span class="filter-label">EVENT LOG</span>
<button class="filter-chip active">all</button>
<button class="filter-chip">context</button>
<button class="filter-chip">tools</button>
<button class="filter-chip">output</button>
</div>
`)

	if hasWebMsg {
		sb.WriteString(`<button class="send-btn" id="send-btn">
<svg width="12" height="12" viewBox="0 0 14 14" fill="currentColor"><path d="M2.198 1.764q-.14.041-.26.154-.12.113-.161.253-.027.099-.027 2.242 0 2.14.027 2.379.085.588.373 1.094.287.502.776.865.561.434 1.316.547.113.027.646.027l5.373 0-1.008 1.008q-.711.728-.868.889-.154.161-.181.232-.068.209.014.427.085.215.28.314.099.041.253.041l.041 0q.113.014.198-.041.109-.072.403-.366.226-.209.995-.981l.239-.236q.95-.954 1.251-1.261.304-.308.332-.362.041-.099.041-.239 0-.14-.055-.24-.055-.14-.317-.355-.301-.301-1.237-1.244-.937-.947-1.261-1.254-.321-.308-.387-.342-.062-.038-.103-.044-.041-.007-.14-.007-.154 0-.253.041-.195.099-.28.318-.082.215-.014.424.027.072.181.232.157.161.868.889l1.008 1.008-2.813 0q-2.813 0-2.967-.027-.574-.072-.995-.448-.42-.379-.533-.94-.027-.109-.027-.448l0-3.415q-.014-.475-.027-.646-.014-.109-.051-.185-.034-.079-.113-.147-.075-.072-.154-.106-.075-.038-.188-.051-.109-.014-.195 0z"/></svg>
send
</button>`)
	}

	sb.WriteString(`</div>
<div class="feed">
<div id="messages-container">
`)

	// Render messages with session grouping
	var currentSessionTime time.Time
	for _, msg := range messages {
		// Extract trigger from content if present
		var triggerLabel, contentToShow string
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "[@") {
			if endIdx := strings.Index(msg.Content, "]\n"); endIdx > 0 {
				triggerLabel = msg.Content[1:endIdx]
				contentToShow = msg.Content[endIdx+2:]
			} else {
				triggerLabel = "USER"
				contentToShow = msg.Content
			}
		} else {
			triggerLabel = "ASSISTANT"
			contentToShow = msg.Content
		}

		// Check for session start (new timestamp significantly different)
		if currentSessionTime.IsZero() || msg.Timestamp.Sub(currentSessionTime) > time.Hour {
			sb.WriteString(fmt.Sprintf(`<div class="session-divider">— session started · %s —</div>`, formatTime(msg.Timestamp)))
			currentSessionTime = msg.Timestamp
		}

		msgClass := "msg-user"
		msgFilter := "context"
		if msg.Role == "assistant" {
			msgClass = "msg-assistant"
			msgFilter = "output"
		}

		// Render tool messages (may contain call + result)
		if msg.Role == "tool" {
			// Split content by newline - first part is call, rest is result
			parts := strings.SplitN(msg.Content, "\n", 2)
			callLine := strings.TrimSpace(parts[0])
			fullResult := ""
			if len(parts) > 1 {
				fullResult = parts[1]
			}

			// Format call line
			if strings.HasPrefix(callLine, "$ ") {
				callLine = callLine[2:]
			}
			// Parse JSON in call
			if idx := strings.Index(callLine, "{"); idx >= 0 {
				jsonPart := callLine[idx:]
				var jsonData map[string]interface{}
				if err := json.Unmarshal([]byte(jsonPart), &jsonData); err == nil {
					var callParts []string
					for k, v := range jsonData {
						callParts = append(callParts, k+"="+fmt.Sprintf("%v", v))
					}
				callLine = callLine[:idx] + strings.Join(callParts, " ")
				}
			}
			callHtml := html.EscapeString(callLine)

			// Generate summary and full output HTML
			var summaryHtml, fullOutputHtml string
			var showExpand bool

			if fullResult != "" {
				showExpand = true
				// Clean up result (remove braces if present)
				resultClean := strings.TrimSpace(fullResult)
				if strings.HasPrefix(resultClean, "{") && strings.HasSuffix(resultClean, "}") {
					resultClean = resultClean[1 : len(resultClean)-1]
				}

				// Generate full output with color coding
				var outputLines []string
				for _, line := range strings.Split(resultClean, "\n") {
					escapedLine := html.EscapeString(line)
					// Color stderr lines (typically start with stderr: or contain error indicators)
					if strings.HasPrefix(line, "stderr:") || strings.HasPrefix(line, "Error:") || strings.HasPrefix(strings.ToLower(line), "error:") {
						outputLines = append(outputLines, `<span class="tool-stderr">`+escapedLine+`</span>`)
					} else if strings.Contains(line, "Permission denied") || strings.Contains(line, "cannot access") || strings.Contains(line, "No such file") {
						outputLines = append(outputLines, `<span class="tool-stderr">`+escapedLine+`</span>`)
					} else {
						outputLines = append(outputLines, `<span class="tool-stdout">`+escapedLine+`</span>`)
					}
				}
				fullOutputHtml = strings.Join(outputLines, "\n")

				// Create summary (truncated)
				if len(resultClean) > 60 {
					summaryHtml = html.EscapeString(resultClean[:60]) + "..."
				} else {
					summaryHtml = html.EscapeString(resultClean)
				}
			}

			// Build the HTML
			sb.WriteString(fmt.Sprintf(`<div class="msg" data-filter="tools"><div class="tool-call" onclick="toggleToolOutput(this)"><span class="tool-toggle">▶</span><span class="tool-cmd">%s</span><span class="tool-summary">%s</span>`, callHtml, summaryHtml))

			if fullResult != "" {
				if showExpand {
					sb.WriteString(fmt.Sprintf(`</div><div class="tool-output" style="display:none">%s</div></div>`, fullOutputHtml))
				} else {
					sb.WriteString(`</div></div>`)
				}
			} else {
				sb.WriteString(`</div></div>`)
			}
			continue
		}

		// Parse content for tool calls
		lines := strings.Split(contentToShow, "\n")
		var mainContent, toolLines []string
		isInDone := false
		for _, line := range lines {
			if strings.Contains(line, "<<<<<DONE>>>>>") {
				isInDone = true
				continue
			}
			if isInDone {
				continue
			}
			if strings.HasPrefix(line, "$ ") || strings.HasPrefix(line, "read ") || strings.HasPrefix(line, "write ") || strings.HasPrefix(line, "edit ") {
				toolLines = append(toolLines, line)
			} else {
				mainContent = append(mainContent, line)
			}
		}

		contentStr := html.EscapeString(strings.TrimSpace(strings.Join(mainContent, "\n")))
		if contentStr != "" {
			contentClass := "msg-content"
			if isInDone {
				contentClass = "msg-content done"
			}
			sb.WriteString(fmt.Sprintf(`<div class="msg %s" data-filter="%s">
<div class="msg-header">
<span class="msg-tag">%s</span>
<span class="msg-time">%s</span>
</div>
<div class="%s">%s</div>`,
				msgClass, msgFilter, html.EscapeString(triggerLabel), formatTime(msg.Timestamp),
				contentClass, contentStr))
		}

		// Render tool calls
		for _, toolLine := range toolLines {
			cleanLine := html.EscapeString(strings.TrimSpace(toolLine))
			icon := `<svg class="tool-icon" viewBox="0 0 14 14" fill="currentColor"><path d="M2.225 2.352q-.195.027-.321.174-.126.147-.147.352-.021.202.092.369.055.072 1.61 1.61l1.552 1.555-1.579 1.583q-1.066 1.063-1.326 1.336-.256.273-.301.342-.109.239-.014.465.099.222.321.321.226.096.465-.014.068-.044.376-.338.307-.294 1.818-1.822 1.863-1.89.055-.113.055-.26 0-.147-.055-.273-.031-.055-1.863-1.89-1.22-1.23-1.542-1.538-.321-.308-.376-.338-.167-.068-.352-.041zm4.635 8.162q-.126.027-.239.133-.109.106-.167.232-.068.209.014.427.085.215.294.314l.113.041 4.146.014q.53-.014.711-.027.126 0 .202-.041.079-.044.147-.12.072-.079.117-.161.161-.096.226.01.448-.082.226-.321.338l.082-.027-2.437-.014q-2.42 0-2.505.014zm-5.643 1.709q-.126.027-.239.133-.109.106-.167.232-.027.068-.027.181 0 .113 0 .629l0 .728.041.085q.044.068.126.154.085.082.161.126.079.041.246.041.167 0 .243-.041.079-.044.161-.126.085-.085.129-.154l.041-.085 0-.728q0-.516 0-.629 0-.113-.027-.181-.085-.198-.273-.301-.188-.106-.413-.065z"/></svg>`
			checkIcon := `<svg class="tool-check" viewBox="0 0 14 14" fill="currentColor"><path d="M11.481 2.953q-.072.014-.13.058-.055.041-3.076 3.066l-3.025 3.008-1.289-1.289q-1.289-1.285-1.381-1.326-.089-.044-.222-.044-.133 0-.232.038-.096.034-.188.113-.089.075-.133.171-.027.072-.034.113-.007.041-.007.14l0 .041q-.014.113.041.198.072.109.366.403.195.212.967.981l1.497 1.483q.28.267.39.338.072.055.185.041l.096.014q.072 0 .14-.027.085-.072.321-.287.239-.219.8-.762l2.283-2.283q2.085-2.099 2.7-2.714.615-.619.646-.687.041-.085.041-.239 0-.099-.007-.14-.007-.041-.034-.113-.044-.082-.137-.164-.085-.085-.181-.12-.089-.037-.208-.037-.12 0-.188.027z"/></svg>`
			// Extract command from line (remove $ prefix for display)
			cmd := cleanLine
			if strings.HasPrefix(cmd, "$ ") {
				cmd = cmd[2:]
			}
			sb.WriteString(fmt.Sprintf(`<div class="msg" data-filter="tools"><div class="tool-call">
%s
<span class="tool-cmd">%s</span>
<span class="tool-status">%s</span>
</div></div>`, icon, cmd, checkIcon))
		}

		if contentStr != "" || len(toolLines) > 0 {
			sb.WriteString(`</div>`)
		}
	}

	sb.WriteString(`</div>
</div>
</div>
`)

	// Queue tab
	sb.WriteString(`<div class="tab-panel" id="panel-queue">
<div class="queue-view">
<div class="queue-head">
<div class="queue-head-left">
<span class="queue-head-label">QUEUE</span>
<span class="queue-head-sub">processed in order, oldest first</span>
</div>
<button class="clear-btn" id="clear-queue">clear all</button>
</div>
<div id="queue-list">
`)

	queuedJobs, _ := s.getQueuedJobs()
	if len(queuedJobs) == 0 {
		sb.WriteString(`<div class="queue-empty">Queue is empty</div>`)
	} else {
		for i, job := range queuedJobs {
			// Determine job type icon
			icon := s.getJobTypeIcon(job)
			meta := s.getJobMeta(job)
			sb.WriteString(fmt.Sprintf(`<div class="queue-item" id="queue-item-%d">
<span class="queue-order">%d</span>
%s
<div class="queue-info">
<span class="queue-label">%s</span>
<span class="queue-meta">%s</span>
</div>
<svg class="queue-icon grip" viewBox="0 0 14 14" fill="currentColor"><path d="M4.997 1.777q-.308.072-.561.308-.181.185-.267.379-.082.195-.082.448 0 .253.075.441.079.188.267.379.191.188.379.267.188.075.441.075.253 0 .441-.075.188-.079.376-.267.191-.191.267-.379.079-.188.079-.441 0-.366-.181-.629-.212-.308-.54-.441-.328-.133-.694-.065z m3.5 0q-.308.072-.561.308-.181.185-.267.379-.082.195-.082.448 0 .253.075.441.079.188.267.379.191.188.379.267.188.075.441.075.253 0 .441-.075.188-.079.376-.267.191-.191.267-.379.079-.188.079-.441 0-.366-.181-.629-.212-.308-.54-.441-.328-.133-.694-.065z m-3.374 4.074q-.209.014-.376.099-.239.113-.4.315-.161.202-.232.441-.027.113-.027.294 0 .181.027.294.085.308.308.533.226.222.533.308.113.027.294.027.181 0 .294-.027.308-.085.53-.308.226-.226.311-.533.027-.113.027-.294 0-.181-.027-.294-.072-.239-.232-.441-.161-.202-.4-.315-.263-.14-.629-.099z m3.5 0q-.209.014-.376.099-.239.113-.4.315-.161.202-.232.441-.027.113-.027.294 0 .181.027.294.085.308.308.533.226.222.533.308.113.027.294.027.181 0 .294-.027.239-.072.441-.232.202-.161.315-.39.113-.232.113-.513 0-.407-.253-.728-.181-.239-.455-.342-.273-.106-.581-.079z m-3.654 4.102q-.28.058-.533.308-.195.198-.273.386-.075.188-.075.441 0 .366.181.629.239.352.608.479.373.123.742 0 .373-.126.612-.479.181-.263.181-.629 0-.253-.079-.441-.075-.188-.27-.386-.253-.25-.547-.308-.113-.027-.273-.027-.161 0-.273.027z m3.5 0q-.28.058-.533.308-.195.198-.273.386-.075.188-.075.441 0 .366.181.629.239.352.608.479.373.123.742 0 .373-.126.612-.479.181-.263.181-.629 0-.253-.079-.441-.075-.188-.27-.386-.253-.25-.547-.308-.113-.027-.273-.027-.161 0-.273.027z"/></svg>
<svg class="queue-icon remove" viewBox="0 0 14 14" fill="currentColor"><path d="M3.346 2.939q-.253.085-.379.308-.041.085-.041.253 0 .167.041.253.044.082 1.624 1.665l1.583 1.583-1.583 1.583q-1.579 1.583-1.624 1.668-.041.082-.041.236 0 .154.034.239.038.082.133.181.099.096.181.133.085.034.239.034.154 0 .236-.041.085-.044 1.668-1.624l1.583-1.583 1.583 1.583q1.583 1.579 1.665 1.624.085.041.239.041.154 0 .236-.034.085-.038.181-.133.099-.099.133-.181.038-.085.038-.239 0-.154-.044-.236-.041-.085-1.62-1.668l-1.583-1.583 1.583-1.583q1.579-1.583 1.62-1.665.044-.085.044-.253 0-.167-.041-.253-.099-.167-.28-.267-.058-.027-.099-.041-.041-.014-.154-.014-.113 0-.154.014-.041.014-.113.041-.099.058-1.665 1.627l-1.569 1.565-2.827-2.813q-.311-.294-.42-.379-.085-.055-.198-.055l-.027 0q-.14 0-.181.014z"/></svg>
</div>`, i+1, icon, html.EscapeString(job.Name), meta))
		}
	}

	sb.WriteString(`</div>
</div>
</div>
`)

	// Config tab
	sb.WriteString(`<div class="tab-panel" id="panel-config">
<div class="config-view">
<div class="config-section">
<span class="config-section-label">SYSTEM PROMPT</span>
<div class="prompt-card">
`)

	if s.systemPrompt != "" {
		// Split system prompt from completion instructions
		promptLines := strings.Split(s.systemPrompt, "\n")
		var mainPrompt, completionHint []string
		isCompletion := false
		for _, line := range promptLines {
			if strings.Contains(line, "<<<<<DONE") {
				isCompletion = true
				continue
			}
			if isCompletion {
				completionHint = append(completionHint, line)
			} else {
				mainPrompt = append(mainPrompt, line)
			}
		}

		if len(mainPrompt) > 0 {
			sb.WriteString(`<div class="prompt-text">` + html.EscapeString(strings.TrimSpace(strings.Join(mainPrompt, "\n"))) + `</div>`)
		}
		if len(completionHint) > 0 {
			sb.WriteString(`<div class="prompt-text secondary">` + html.EscapeString(strings.TrimSpace(strings.Join(completionHint, "\n"))) + `</div>`)
		}
	} else {
		sb.WriteString(`<div class="prompt-text secondary">No system prompt configured</div>`)
	}

	sb.WriteString(`<div class="prompt-card-footer">
<button class="action-btn" id="edit-prompt">
<svg viewBox="0 0 14 14" fill="currentColor"><path d="M11.002 0.602q-.615.041-1.145.434-.154.099-4.132 4.088-3.975 3.989-4.088 4.129-.14.198-.232.458-.089.256-.448 1.463-.355 1.203-.369 1.285-.014.126.021.273.034.147.089.246.058.099.171.205.113.103.208.147.099.041.226.068.126.027.229.014.106-.014 1.381-.393l1.248-.39q.263-.085.362-.126l.014-.014q.14-.072.321-.226.253-.239 1.08-1.049l2.926-2.926q3.989-3.992 4.102-4.146.181-.25.294-.516.239-.602.12-1.23-.12-.632-.567-1.107-.352-.366-.813-.547-.461-.181-.995-.14z m.42 1.176q.28.058.509.291.232.229.291.509.082.376-.085.714-.041.082-.133.181-.089.096-.427.448l-.502.502-1.497-1.497.502-.502q.352-.338.448-.427.099-.092.181-.133.338-.167.714-.085z m-4.242 6.539q-3.08 3.08-3.145 3.117-.062.034-1.084.349-1.022.314-1.036.308-.014-.007.294-1.029.308-1.022.335-1.07.031-.051 3.124-3.158l3.093-3.093 1.483 1.497-3.066 3.08z"/></svg>
edit
</button>
</div>
</div>
</div>

<div class="config-section">
<span class="config-section-label">CONFIGURATION</span>
<div class="config-card">
<div class="config-row">
<span class="config-row-label">Model</span>
<span class="config-row-value">` + html.EscapeString(s.modelName) + `</span>
</div>
<div class="config-row">
<span class="config-row-label">Done token</span>
<span class="config-row-value">` + html.EscapeString(s.doneToken) + `</span>
</div>
<div class="config-row">
<span class="config-row-label">Workspace</span>
<span class="config-row-value">` + html.EscapeString(s.workspace) + `</span>
</div>
<div class="config-row">
<span class="config-row-label">Container</span>
<span class="config-row-value">` + html.EscapeString(s.containerID) + `</span>
</div>
</div>
</div>

<div class="config-section">
<span class="config-section-label">SESSION</span>
<div class="session-actions">
<button class="session-btn" id="restart-session">
<svg viewBox="0 0 14 14" fill="currentColor"><path d="M1.596 1.189q-.253.085-.379.308l-.041.085 0 3.261.041.072q.099.195.294.28l.085.041 2.813.014q.321-.014.434-.027.072-.014.154-.072l.014-.014q.171-.126.219-.314.048-.188-.031-.376-.075-.191-.256-.287-.085-.058-.267-.072-.181-.014-.769-.014l-.742 0 .236-.226q.742-.728 1.583-1.09.967-.42 2.03-.42.94 0 1.764.349 1.176.492 1.938 1.494.766 1.001.919 2.246.027.226.027.567 0 .342-.027.567-.154 1.275-.94 2.29-.783 1.012-1.972 1.49-.813.321-1.723.321-.28 0-.461-.014-.181-.014-.461-.072-.882-.181-1.647-.684-.762-.506-1.282-1.254-.516-.749-.711-1.644-.085-.448-.092-.776-.007-.332-.075-.472-.085-.167-.267-.267-.058-.027-.099-.041-.041-.014-.154-.014-.113 0-.154.014-.041.014-.099.041-.181.099-.28.267-.068.154-.034.653.034.496.147.988.294 1.189 1.036 2.157.742.964 1.805 1.559 1.066.595 2.297.708.185.014.533.014.349 0 .533-.014 1.384-.126 2.546-.868 1.148-.714 1.863-1.863.742-1.162.868-2.546.014-.185.014-.533 0-.349-.014-.533-.14-1.524-1.015-2.782-.875-1.261-2.263-1.935-.588-.28-1.148-.42-.557-.14-1.217-.154-1.09-.027-2.112.308-.714.239-1.302.605-.588.362-1.148.909l-.28.263 0-1.678-.041-.085q-.099-.181-.28-.267-.085-.041-.226-.048-.14-.007-.181.007z"/></svg>
restart session
</button>
<button class="session-btn" id="export-log">
<svg viewBox="0 0 14 14" fill="currentColor"><path d="M6.846 1.189q-.253.085-.379.308l-.041.085 0 5.752-.995-.995q-.629-.629-.834-.817-.202-.188-.256-.219-.198-.082-.4-.024-.202.055-.328.215-.126.161-.113.369l0 .027q.014.113.068.198.072.113.366.42l1.203 1.203q1.528 1.524 1.596 1.569.058.027.106.034.048.007.161.007.113 0 .161-.007.048-.007.106-.034.068-.044 1.596-1.569 1.036-1.036 1.285-1.302.253-.267.297-.335.096-.226.01-.448-.082-.226-.321-.338-.082-.027-.222-.027l-.027 0q-.126 0-.198.027-.096.058-.308.253-.154.154-.697.701l-1.107 1.09 0-5.752-.041-.085q-.099-.181-.28-.267-.085-.041-.226-.048-.14-.007-.181.007z m-5.25 7q-.253.085-.379.308l-.041.085.014 2.604q0 .236.027.308.14.489.468.82.332.328.82.468.085.027.714.027l3.78.014 3.78-.014q.629 0 .714-.027.489-.14.817-.468.332-.332.472-.82.027-.072.027-.308l.014-2.604-.041-.085q-.099-.167-.28-.267-.058-.027-.099-.041-.041-.014-.154-.014-.113 0-.154.014-.041.014-.099.041-.181.099-.28.267l-.041.085-.014 2.632-.041.099q-.14.277-.434.335-.099.027-4.187.027-4.088 0-4.187-.027-.294-.058-.434-.335l-.041-.099-.014-2.632-.041-.085q-.099-.181-.28-.267-.085-.041-.226-.048-.14-.007-.181.007z"/></svg>
export log
</button>
</div>
</div>
</div>
</div>
`)

	// Status bar (outside tabs)
	sb.WriteString(`<div class="statusbar">
<div class="status-left">
<span class="status-stat">queue ` + fmt.Sprintf("%d", queueLen) + `</span>
<span class="status-stat">` + fmt.Sprintf("%d", meta.MessageCount) + ` messages</span>
</div>
<div class="status-right">
`)

	if s.modelName != "" {
		sb.WriteString(`<span class="model-info">` + html.EscapeString(s.modelName) + ` · docker ` + html.EscapeString(s.containerID) + `</span>`)
	}

	sb.WriteString(`</div>
</div>
</div>
</div>
</div>

<div class="modal" id="msg-modal">
<div class="modal-content">
<h2>Send Message</h2>
<form id="msg-form">
<textarea id="msg-input" placeholder="Type your message..."></textarea>
<div class="modal-actions">
<button type="button" class="modal-btn modal-btn-secondary" id="close-modal">Cancel</button>
<button type="submit" class="modal-btn modal-btn-primary">Send</button>
</div>
</form>
</div>
</div>

<div class="modal" id="prompt-modal">
<div class="modal-content">
<h2>Edit System Prompt</h2>
<form id="prompt-form">
<textarea id="prompt-input" placeholder="Enter system prompt..."></textarea>
<div class="modal-actions">
<button type="button" class="modal-btn modal-btn-secondary" id="close-prompt">Cancel</button>
<button type="submit" class="modal-btn modal-btn-primary">Save</button>
</div>
</form>
</div>
</div>

<script>
const sendBtn = document.getElementById('send-btn');
const msgModal = document.getElementById('msg-modal');
const closeModal = document.getElementById('close-modal');
const msgForm = document.getElementById('msg-form');
const msgInput = document.getElementById('msg-input');
const editPrompt = document.getElementById('edit-prompt');
const promptModal = document.getElementById('prompt-modal');
const closePrompt = document.getElementById('close-prompt');
const promptForm = document.getElementById('prompt-form');
const promptInput = document.getElementById('prompt-input');

// Tab switching
document.querySelectorAll('.tab').forEach(tab => {
    tab.addEventListener('click', () => {
        const tabName = tab.dataset.tab;
        
        document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
        tab.classList.add('active');
        
        document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
        document.getElementById('panel-' + tabName).classList.add('active');
    });
});

// Filter chips
let activeFilter = 'all';
document.querySelectorAll('.filter-chip').forEach(chip => {
    chip.addEventListener('click', () => {
        activeFilter = chip.textContent;
        document.querySelectorAll('.filter-chip').forEach(c => c.classList.remove('active'));
        chip.classList.add('active');
        
        document.querySelectorAll('.msg').forEach(msg => {
            const filter = msg.dataset.filter;
            if (activeFilter === 'all' || filter === activeFilter) {
                msg.style.display = '';
            } else {
                msg.style.display = 'none';
            }
        });
    });
});

// Send message modal
if (sendBtn) {
    sendBtn.addEventListener('click', () => {
        msgModal.classList.add('open');
        msgInput.focus();
    });
}
if (closeModal) {
    closeModal.addEventListener('click', () => {
        msgModal.classList.remove('open');
        msgInput.value = '';
    });
}
msgModal.addEventListener('click', (e) => {
    if (e.target === msgModal) {
        msgModal.classList.remove('open');
        msgInput.value = '';
    }
});
if (msgForm) {
    msgForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const message = msgInput.value.trim();
        if (!message) return;
        
        const submitBtn = msgForm.querySelector('button[type="submit"]');
        submitBtn.disabled = true;
        
        try {
            const resp = await fetch('/web-message', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ message })
            });
            if (resp.ok) {
                msgModal.classList.remove('open');
                msgInput.value = '';
            }
        } finally {
            submitBtn.disabled = false;
        }
    });
}
msgInput.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        msgForm.dispatchEvent(new Event('submit'));
    }
});

// Edit prompt modal
if (editPrompt) {
    editPrompt.addEventListener('click', () => {
        promptModal.classList.add('open');
        promptInput.focus();
    });
}
if (closePrompt) {
    closePrompt.addEventListener('click', () => {
        promptModal.classList.remove('open');
    });
}
promptModal.addEventListener('click', (e) => {
    if (e.target === promptModal) {
        promptModal.classList.remove('open');
    }
});

function connectSSE() {
    const events = new EventSource('/events');
    const sseDot = document.getElementById('sse-dot');
    const sseText = document.getElementById('sse-text');
    
    events.addEventListener('connected', () => {
        sseDot.classList.remove('disconnected');
        sseText.textContent = 'LIVE';
    });

    events.addEventListener('user', (e) => {
        const data = JSON.parse(e.data);
        const trigger = data.content && data.content.trigger || 'USER';
        const content = data.content && data.content.content || '';
        const msg = document.createElement('div');
        msg.className = 'msg msg-user live-message';
        msg.dataset.filter = 'context';
        msg.innerHTML = '<div class="msg-header"><span class="msg-tag">' + escapeHtml(trigger) + '</span><span class="msg-time">' + new Date().toLocaleString() + '</span></div><div class="msg-content">' + escapeHtml(content) + '</div>';
        document.getElementById('messages-container').appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('agent', (e) => {
        const data = JSON.parse(e.data);
        const content = data.content && data.content.content || data.content || '';
        const msg = document.createElement('div');
        msg.className = 'msg msg-assistant live-message';
        msg.dataset.filter = 'output';
        msg.innerHTML = '<div class="msg-header"><span class="msg-tag">ASSISTANT</span><span class="msg-time">' + new Date().toLocaleString() + '</span></div><div class="msg-content">' + escapeHtml(content) + '</div>';
        document.getElementById('messages-container').appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('tool', (e) => {
        const data = JSON.parse(e.data);
        const toolName = data.content && data.content.tool || '';
        const toolInput = data.content && data.content.content || '';
        const msg = document.createElement('div');
        msg.className = 'msg msg-assistant live-message';
        msg.dataset.filter = 'tools';
        msg.innerHTML = '<div class="tool-call"><svg class="tool-icon" viewBox="0 0 14 14" fill="#22D3EE"><path d="M2.225 2.352q-.195.027-.321.174-.126.147-.147.352-.021.202.092.369.055.072 1.61 1.61l1.552 1.555-1.579 1.583q-1.066 1.063-1.326 1.336-.256.273-.301.342-.109.239-.014.465.099.222.321.321.226.096.465-.014.068-.044.376-.338.307-.294 1.818-1.822 1.863-1.89.055-.113.055-.26 0-.147-.055-.273-.031-.055-1.863-1.89-1.22-1.23-1.542-1.538-.321-.308-.376-.338-.167-.068-.352-.041z"/></svg><span class="tool-cmd">' + escapeHtml(toolName + ' ' + toolInput) + '</span><span class="tool-status"><svg class="tool-check" viewBox="0 0 14 14" fill="#34D399"><path d="M11.481 2.953q-.072.014-.13.058-.055.041-3.076 3.066l-3.025 3.008-1.289-1.289q-1.289-1.285-1.381-1.326-.089-.044-.222-.044-.133 0-.232.038-.096.034-.188.113-.089.075-.133.171-.027.072-.034.113-.007.041-.007.14l0 .041q-.014.113.041.198.072.109.366.403.195.212.967.981l1.497 1.483q.28.267.39.338.072.055.185.041l.096.014q.072 0 .14-.027.085-.072.321-.287.239-.219.8-.762l2.283-2.283q2.085-2.099 2.7-2.714.615-.619.646-.687.041-.085.041-.239 0-.099-.007-.14-.007-.041-.034-.113-.044-.082-.137-.164-.085-.085-.181-.12-.089-.037-.208-.037-.12 0-.188.027z"/></svg></span></div>';
        document.getElementById('messages-container').appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('tool_result', (e) => {
        const data = JSON.parse(e.data);
        const toolResult = data.content && data.content.content || '';
        const toolName = data.content && data.content.tool || '';
        // Find the last tool-call div and update it with expandable result
        const toolCalls = document.querySelectorAll('.tool-call');
        if (toolCalls.length > 0) {
            const lastToolCall = toolCalls[toolCalls.length - 1];
            // Check if it already has a toggle (already updated)
            if (!lastToolCall.querySelector('.tool-toggle')) {
                const statusSpan = lastToolCall.querySelector('.tool-status');
                const statusIcon = statusSpan ? statusSpan.innerHTML : '';
                const isError = toolResult.includes('Error') || toolResult.includes('error:');
                // Create summary (truncated)
                let summary = toolResult;
                if (summary.length > 60) {
                    summary = summary.substring(0, 60) + '...';
                }
                summary = escapeHtml(summary).replace(/\n/g, ' ');
                // Color code output
                const lines = toolResult.split('\n');
                let outputHtml = '';
                for (const line of lines) {
                    const escapedLine = escapeHtml(line);
                    if (line.startsWith('Error:') || line.startsWith('stderr:') || line.includes('Permission denied') || line.includes('No such file')) {
                        outputHtml += '<span class="tool-stderr">' + escapedLine + '</span>\n';
                    } else {
                        outputHtml += '<span class="tool-stdout">' + escapedLine + '</span>\n';
                    }
                }
                // Update the tool call HTML
                lastToolCall.innerHTML = lastToolCall.innerHTML.replace(
                    '<span class="tool-cmd">' + escapeHtml(toolName + ' ' + (data.content && data.content.content || '')) + '</span><span class="tool-status">',
                    '<span class="tool-cmd">' + escapeHtml(toolName + ' ' + (data.content && data.content.content || '')) + '</span><span class="tool-toggle" onclick="toggleToolOutput(this)">▶</span><span class="tool-summary">' + summary + '</span></div><div class="tool-output" style="display:none">' + outputHtml
                );
            }
        }
    });

    events.addEventListener('error', (e) => {
        const data = JSON.parse(e.data);
        const msg = document.createElement('div');
        msg.className = 'msg msg-assistant live-message';
        msg.innerHTML = '<div class="msg-header"><span class="msg-tag" style="color:#f44336">ERROR</span><span class="msg-time">' + new Date().toLocaleString() + '</span></div><div class="msg-content">' + escapeHtml(data.content && data.content.message || data.message || '') + '</div>';
        document.getElementById('messages-container').appendChild(msg);
        msg.scrollIntoView({ behavior: 'smooth' });
    });

    events.addEventListener('queue', (e) => {
        const data = JSON.parse(e.data);
        const queueBadge = document.getElementById('queue-badge');
        if (data.action === 'enqueue') {
            queueBadge.textContent = parseInt(queueBadge.textContent) + 1;
        } else if (data.action === 'dequeue') {
            queueBadge.textContent = Math.max(0, parseInt(queueBadge.textContent) - 1);
        }
    });

    events.onerror = () => {
        sseDot.classList.add('disconnected');
        sseText.textContent = 'DISCONNECTED';
        setTimeout(connectSSE, 3000);
    };
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function toggleToolOutput(el) {
    // el can be the tool-call div or the toggle icon
    const toolCall = el.classList.contains('tool-call') ? el : el.closest('.tool-call');
    const output = toolCall.nextElementSibling;
    const toggle = toolCall.querySelector('.tool-toggle');
    if (output && output.classList.contains('tool-output')) {
        if (output.style.display === 'none') {
            output.style.display = 'block';
            if (toggle) {
                toggle.classList.add('expanded');
                toggle.textContent = '▼';
            }
        } else {
            output.style.display = 'none';
            if (toggle) {
                toggle.classList.remove('expanded');
                toggle.textContent = '▶';
            }
        }
    }
}

connectSSE();
</script>
</body>
</html>
`)

	return sb.String()
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
	for _, job := range jobs {
		s.queue.Enqueue(job)
	}
	return jobs, nil
}

func (s *Server) getJobTypeIcon(job queue.Job) string {
	// Determine icon based on job type/name pattern
	if strings.HasPrefix(job.Name, "http://") || strings.HasPrefix(job.Name, "https://") {
		return `<svg class="queue-icon" viewBox="0 0 14 14" fill="currentColor"><path d="M9.365 0.673q-.53.068-.995.294-.335.167-.629.39-.167.126-.769.728-.602.602-.632.673-.096.181-.062.366.034.181.167.314.133.13.328.161.198.027.366-.072.055-.027.643-.608.588-.581.728-.68.547-.376 1.176-.434.687-.055 1.275.253.588.308.94.909.28.479.308 1.053.027.571-.212 1.09-.096.181-.167.294-.099.14-.335.393-.181.181-.786.783-1.005 1.022-1.131 1.107-.352.28-.8.4-.448.116-.868.062-.937-.113-1.538-.841-.126-.14-.205-.195-.075-.058-.174-.085-.222-.068-.434.044-.209.109-.28.335-.041.167-.007.308.034.14.161.308.646.813 1.624 1.135.981.321 1.976.068.728-.195 1.343-.67.126-.099 1.128-1.101 1.001-1.001 1.097-1.128.588-.728.742-1.678.044-.181.044-.523 0-.345-.044-.526-.195-1.176-1.005-1.962-.407-.407-.913-.656-.502-.253-1.077-.321-.212-.031-.506-.031-.294 0-.475.044z m-4.016 4.03q-.926.126-1.682.701-.126.113-1.107 1.087-.978.971-1.131 1.138-.506.619-.701 1.374-.055.25-.079.427-.021.174-.021.455 0 .28.014.448.017.167.085.42.181.728.677 1.33.499.601 1.172.937.728.352 1.524.352.841 0 1.569-.366.335-.167.564-.349.232-.181.779-.742.434-.434.54-.54.106-.106.133-.174.068-.226-.01-.434-.075-.212-.284-.308-.099-.058-.239-.058l-.014 0q-.14 0-.212.031-.068.027-.208.154-.099.096-.448.448l-.027.027q-.602.588-.728.687-.352.25-.772.369-.417.12-.824.079-.588-.058-1.06-.352-.468-.294-.755-.783-.287-.492-.314-1.07-.027-.581.212-1.101.096-.181.167-.294.099-.14.335-.393.181-.181.769-.783 1.008-1.008 1.135-1.107.533-.403 1.234-.475.561-.041 1.09.185.533.222.909.684.113.126.188.185.079.055.178.082.154.041.308 0 .154-.041.273-.161.12-.12.147-.294.027-.178-.041-.332-.031-.082-.164-.243-.13-.161-.27-.301-.407-.393-.926-.636-.516-.246-1.09-.318-.14-.014-.448-.014-.308 0-.448.027z"/></svg>`
	}
	return `<svg class="queue-icon" viewBox="0 0 14 14" fill="currentColor"><path d="M5.124 0.588q-.181.027-.308.079-.126.048-.28.161-.393.294-.448.824l-.014.099-.335 0q-.52 0-.786.085-.461.167-.769.52-.308.349-.407.81-.014.126-.027.714l0 6.969q.014.8.027 1.039.014.181.058.308l.014.027q.14.448.489.755.349.308.813.407.109.014.67.027l3.179 0 3.179 0q.561-.014.67-.027.465-.099.813-.407.349-.308.489-.755l.014-.027q.044-.126.058-.308.014-.239.027-1.039l0-6.969q-.014-.588-.027-.714-.099-.461-.407-.81-.308-.352-.769-.52-.267-.085-.786-.085l-.335 0-.014-.099q-.041-.321-.212-.564-.167-.246-.448-.386l-.027-.014q-.096-.058-.195-.072-.126-.014-.489-.027l-1.456 0q-1.835-.014-1.962 0z m3.626 1.75l0 .588-3.5 0 0-1.176 3.5 0 0 .588z m-4.662.701q.027.209.103.369.079.161.246.332.239.236.533.308.126.027 2.03.027 1.904 0 2.03-.027.294-.072.533-.308.167-.171.243-.332.079-.161.106-.383l.014-.099.366 0q.277 0 .338.007.065.007.123.048.181.085.267.267l.055.085 0 8.442-.027.096q-.044.099-.13.191-.082.089-.164.133l-.099.041-7.308 0-.099-.041q-.082-.044-.167-.133-.082-.092-.126-.191l-.027-.096 0-8.442.055-.085q.126-.236.366-.308.041-.014.379-.014l.349 0 .014.113z"/></svg>`
}

func (s *Server) getJobMeta(job queue.Job) string {
	// Simple meta based on job name
	if strings.HasPrefix(job.Name, "http://") || strings.HasPrefix(job.Name, "https://") {
		return "link · added " + formatTime(job.EnqueuedAt)
	}
	return "job · added " + formatTime(job.EnqueuedAt)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format("Jan 02 15:04:05")
}
