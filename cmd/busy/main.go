package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nalanj/busy/internal/agent"
	"github.com/nalanj/busy/internal/config"
	"github.com/nalanj/busy/internal/queue"
	"github.com/nalanj/busy/internal/scheduler"
	"github.com/nalanj/busy/internal/watcher"
	"github.com/nalanj/busy/internal/web"
)

// LogEntry represents a structured log entry
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func init() {
	// Set agent name for logging context
}

func log(level, logType, message string) {
	entry := LogEntry{
		Time:    time.Now().Format(time.RFC3339),
		Level:   level,
		Type:    logType,
		Message: message,
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
}

func main() {
	// Subcommand: `busy one-shot --config … --prompt … --out …`
	// Run the agent once with the given prompt and write the final
	// assistant text to --out. No daemon, no jobs, no web server —
	// the model runs once and the process exits. Intended for shell
	// scripts and other one-shot drivers that want busy to handle the
	// LLM call but not the orchestration.
	if len(os.Args) > 1 && os.Args[1] == "one-shot" {
		if err := runOneShot(os.Args[2:]); err != nil {
			log("error", "one-shot", err.Error())
			os.Exit(1)
		}
		return
	}

	configPath := flag.String("config", "", "Path to agent config file")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		flag.Usage()
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log("error", "config", fmt.Sprintf("failed to load config: %v", err))
		os.Exit(1)
	}

	if cfg.Agent.Name == "" {
		log("error", "config", "agent name is required")
		os.Exit(1)
	}

	log("info", "config", fmt.Sprintf("loaded config from %s", *configPath))

	runner, err := agent.New(context.Background(), &cfg.Agent)
	if err != nil {
		log("error", "agent", fmt.Sprintf("failed to create runner: %v", err))
		os.Exit(1)
	}
	defer runner.Close()

	// Create job queue
	jobQueue, err := queue.New(cfg.Agent.GetStateDir())
	if err != nil {
		log("error", "queue", fmt.Sprintf("failed to create job queue: %v", err))
		os.Exit(1)
	}

	// Start web server if configured
	if cfg.Agent.ListenAddr != "" {
		server := web.New(cfg.Agent.ListenAddr, runner.Store(), jobQueue, cfg.Agent.Name)
		runner.SetEmitter(server.SSEHub())
		web.SetGlobalEmitter(server.SSEHub())
		server.SetPathPrefix(cfg.Agent.PathPrefix)
		server.SetSystemPrompt(runner.SystemPrompt())
		server.SetModelName(cfg.Agent.Model)
		server.SetDoneToken("<<<<<DONE>>>>>")

		// Get workspace path
		home, _ := os.UserHomeDir()
		workspace := ""
		if home != "" {
			workspace = filepath.Join(home, ".local", "share", "busy", cfg.Agent.Name, "workspace")
		}
		server.SetWorkspace(workspace)

		// Get container ID
		containerID := "unknown"
		if data, err := os.ReadFile("/etc/hostname"); err == nil {
			containerID = strings.TrimSpace(string(data))
		}
		server.SetContainerID(containerID)

		// Pass job info to server
		var jobInfos []struct{ Name, Schedule, Prompt string }
		for _, job := range cfg.Jobs {
			jobInfos = append(jobInfos, struct{ Name, Schedule, Prompt string }{job.Name, job.Schedule, job.Prompt})
		}
		server.SetJobs(jobInfos)

		// Configure web-message jobs
		var webMsgJobs []queue.WebMessageJob
		for _, job := range cfg.Jobs {
			if job.Schedule == "@web-message" {
				webMsgJobs = append(webMsgJobs, queue.WebMessageJob{
					Name:   job.Name,
					Prompt: job.Prompt,
				})
			}
		}
		if len(webMsgJobs) > 0 {
			server.SetWebMessageJobs(webMsgJobs, func(job queue.Job) {
				jobQueue.Enqueue(job)
			})
		}

		go func() {
			log("info", "web", fmt.Sprintf("starting on http://%s", cfg.Agent.ListenAddr))
			if err := server.Start(); err != nil {
				log("error", "web", fmt.Sprintf("server error: %v", err))
			}
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log("info", "system", "shutting down")
		cancel()
	}()

	// Track running jobs and queue for scheduling
	runningJobs := make(map[string]bool)
	var jobsMu sync.Mutex
	var wg sync.WaitGroup

	// Start job worker that processes the queue
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Try to dequeue a job
				queuedJob, err := jobQueue.Dequeue()
				if err != nil {
					log("error", "queue", fmt.Sprintf("dequeue error: %v", err))
					time.Sleep(time.Second)
					continue
				}

				if queuedJob == nil {
					// Queue empty, sleep briefly and retry
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// Check if job is already running
				jobsMu.Lock()
				if runningJobs[queuedJob.Name] {
					// Re-queue the job
					log("warn", "queue", fmt.Sprintf("job %s already running, re-queuing", queuedJob.Name))
					jobQueue.Enqueue(*queuedJob)
					jobsMu.Unlock()
					time.Sleep(time.Second)
					continue
				}
				runningJobs[queuedJob.Name] = true
				jobsMu.Unlock()

				// Preconditions: if any returns non-zero, skip the job
				// without running the LLM. This is what makes
				// "@watch /inbox/briefs/*.md" cheap when nothing has
				// changed — the watcher still fires, the script
				// answers "is there work to do?", and we save the
				// token spend.
				if job, ok := findJob(cfg.Jobs, queuedJob.Name); ok && len(job.Preconditions) > 0 {
					skip, reason := checkPreconditions(job.Preconditions)
					if skip {
						log("info", "queue", fmt.Sprintf("skip %s: %s", queuedJob.Name, reason))
						jobsMu.Lock()
						delete(runningJobs, queuedJob.Name)
						jobsMu.Unlock()
						continue
					}
				}

				// Run the job
				log("info", "queue", fmt.Sprintf("dequeued job %s", queuedJob.Name))
				web.EmitGlobal("queue", map[string]interface{}{"action": "dequeue", "job": queuedJob})
				runJobFromQueue(ctx, runner, queuedJob)

				// Mark job as done
				jobsMu.Lock()
				delete(runningJobs, queuedJob.Name)
				jobsMu.Unlock()
			}
		}
	}()

	// Set up file watcher for any @watch schedules. The watcher
	// polls at 1-second intervals and dispatches to per-watch
	// callbacks (which enqueue jobs into the regular queue).
	fileWatcher := watcher.New(time.Second)

	sched := scheduler.New()

	for _, job := range cfg.Jobs {
		job := job // capture range variable

		schedule := job.Schedule
		if schedule == "@once" {
			// @once always runs once
			log("info", "scheduler", fmt.Sprintf("job %s queued for one-time execution", job.Name))
			enqJob := queue.Job{
				Name:       job.Name,
				Prompt:     job.Prompt,
				Trigger:    schedule,
				EnqueuedAt: time.Now(),
			}
			jobQueue.Enqueue(enqJob)
			web.EmitGlobal("queue", map[string]interface{}{"action": "enqueue", "job": enqJob})
		} else if schedule == "@session-start" {
			// @session-start only runs on first session (no existing messages)
			messages, _ := runner.Store().GetMessages()
			if len(messages) == 0 {
				log("info", "scheduler", fmt.Sprintf("job %s queued for first session", job.Name))
				enqJob := queue.Job{
					Name:       job.Name,
					Prompt:     job.Prompt,
					Trigger:    schedule,
					EnqueuedAt: time.Now(),
				}
				jobQueue.Enqueue(enqJob)
				web.EmitGlobal("queue", map[string]interface{}{"action": "enqueue", "job": enqJob})
			} else {
				log("info", "scheduler", fmt.Sprintf("job %s skipped (session already exists with %d messages)", job.Name, len(messages)))
			}
		} else if schedule == "@web-message" {
			// @web-message is triggered via web API, not scheduler
			log("info", "scheduler", fmt.Sprintf("job %s waiting for web input", job.Name))
		} else if watchGlob, isWatch := isWatchSchedule(schedule); isWatch {
			// @watch <glob> — file events drive the job. The watcher
			// polls and calls this callback when matching files change.
			// The watcher fires on file events. Each job is responsible
			// for any deduplication or state-tracking it needs (e.g.
			// a brief-watcher job might inspect /inbox/briefs/ itself).
			// so this is a drop-in replacement for @every polling.
			jobName := job.Name
			jobPrompt := job.Prompt
			fileWatcher.Add(&watcher.Watch{
				Dir:      filepath.Dir(watchGlob),
				Pattern:  filepath.Base(watchGlob),
				Debounce: time.Second,
				OnChange: func(paths []string) {
					log("info", "watch", fmt.Sprintf("%s: %d file(s) changed", jobName, len(paths)))
					enqJob := queue.Job{
						Name:       jobName,
						Prompt:     jobPrompt,
						Trigger:    "@watch",
						EnqueuedAt: time.Now(),
					}
					jobQueue.Enqueue(enqJob)
					web.EmitGlobal("queue", map[string]interface{}{"action": "enqueue", "job": enqJob})
				},
			})
			log("info", "scheduler", fmt.Sprintf("job %s watching %s", jobName, watchGlob))
		} else {
			// Schedule recurring jobs
			_, err := sched.Add(job.Name, schedule, func() {
				enqJob := queue.Job{
					Name:       job.Name,
					Prompt:     job.Prompt,
					Trigger:    schedule,
					EnqueuedAt: time.Now(),
				}
				jobQueue.Enqueue(enqJob)
				web.EmitGlobal("queue", map[string]interface{}{"action": "enqueue", "job": enqJob})
			})
			if err != nil {
				log("error", "scheduler", fmt.Sprintf("failed to schedule job %s: %v", job.Name, err))
			}
		}
	}

	if len(cfg.Jobs) == 0 {
		log("warn", "scheduler", "no jobs configured")
		<-ctx.Done()
		return
	}

	sched.Start()
	fileWatcher.Start(ctx)
	wg.Wait()
	fileWatcher.Stop()
}

func runJobFromQueue(ctx context.Context, runner *agent.Runner, job *queue.Job) {
	trigger := job.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	log("info", "job", fmt.Sprintf("starting job %s (trigger: %s)", job.Name, trigger))

	if err := runner.Run(ctx, job.Prompt, job.Trigger); err != nil {
		log("error", "job", fmt.Sprintf("job %s failed: %v", job.Name, err))
	} else {
		log("info", "job", fmt.Sprintf("job %s completed", job.Name))
	}
}

// isWatchSchedule checks whether schedule is an @watch directive and
// returns the glob. It's a thin wrapper around scheduler's parser
// that turns the typed error into a friendly (glob, ok) signature.
func isWatchSchedule(schedule string) (string, bool) {
	_, err := scheduler.Parse(schedule)
	if err == nil {
		return "", false
	}
	var wsErr *scheduler.WatchScheduleError
	if errors.As(err, &wsErr) {
		return wsErr.Glob, true
	}
	return "", false
}

// runOneShot implements `busy one-shot`. Loads the agent config, runs
// the agent once with the supplied prompt, then writes the final
// assistant text to the output path (after stripping the trailing
// DONE marker). The runner's session JSONL is not touched on exit —
// it's a transient one-shot call, not a persistent agent loop.
func runOneShot(args []string) error {
	fs := flag.NewFlagSet("one-shot", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to agent config file")
	prompt := fs.String("prompt", "", "Prompt to send to the agent")
	outPath := fs.String("out", "", "Path to write the agent's response")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("--config is required")
	}
	if *prompt == "" {
		return fmt.Errorf("--prompt is required")
	}
	if *outPath == "" {
		return fmt.Errorf("--out is required")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg.Agent.Name == "" {
		return fmt.Errorf("agent.name is required in config")
	}

	log("info", "one-shot", fmt.Sprintf("loaded config from %s", *configPath))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner, err := agent.New(ctx, &cfg.Agent)
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}
	defer runner.Close()

	if err := runner.Run(ctx, *prompt, "one-shot"); err != nil {
		return fmt.Errorf("agent run failed: %w", err)
	}

	msgs, err := runner.Store().GetMessages()
	if err != nil {
		return fmt.Errorf("reading messages: %w", err)
	}

	var lastText string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].Content != "" {
			lastText = msgs[i].Content
			break
		}
	}
	if lastText == "" {
		return fmt.Errorf("no assistant text in final messages")
	}

	// Strip the trailing DONE marker the runner expects the model to
	// emit on its own line; the plan file shouldn't carry that token.
	lastText = strings.TrimSuffix(lastText, "<<<<<DONE>>>>>")
	lastText = strings.TrimSpace(lastText)

	if err := os.WriteFile(*outPath, []byte(lastText+"\n"), 0644); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	log("info", "one-shot", fmt.Sprintf("wrote %d bytes to %s", len(lastText), *outPath))
	return nil
}

// findJob looks up a job by name in the slice. Returns the job and
// true on hit, zero value and false on miss.
func findJob(jobs []config.JobConfig, name string) (config.JobConfig, bool) {
	for _, j := range jobs {
		if j.Name == name {
			return j, true
		}
	}
	return config.JobConfig{}, false
}

// checkPreconditions runs each precondition through `sh -c`. If any
// returns non-zero, the job should be skipped. The first failing
// command's stderr is captured as the reason for the log. An empty
// preconditions list is treated as "pass" (true).
func checkPreconditions(conds []string) (skip bool, reason string) {
	for i, cond := range conds {
		out, err := runShellCapture(cond)
		if err != nil {
			return true, fmt.Sprintf("precondition %d failed: %v", i, err)
		}
		if out.ExitCode != 0 {
			msg := strings.TrimSpace(out.Stdout)
			if msg == "" {
				msg = strings.TrimSpace(out.Stderr)
			}
			if msg == "" {
				msg = fmt.Sprintf("exit %d", out.ExitCode)
			}
			return true, fmt.Sprintf("precondition %d (%s)", i, msg)
		}
	}
	return false, ""
}

// shellResult captures stdout/stderr/exit-code from a shell command.
type shellResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runShellCapture runs a command via `sh -c` and returns its output and
// exit code. Used to evaluate preconditions.
func runShellCapture(cmd string) (shellResult, error) {
	c := exec.Command("sh", "-c", cmd)
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	r := shellResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		// `*exec.ExitError` carries the exit code; surface it.
		if ee, ok := err.(*exec.ExitError); ok {
			r.ExitCode = ee.ExitCode()
			return r, nil
		}
		return r, err
	}
	r.ExitCode = 0
	return r, nil
}
