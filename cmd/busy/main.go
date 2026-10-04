package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/nalanj/busy/internal/agent"
	"github.com/nalanj/busy/internal/briefwatcher"
	"github.com/nalanj/busy/internal/config"
	"github.com/nalanj/busy/internal/queue"
	"github.com/nalanj/busy/internal/scheduler"
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

				// Pre-flight check for the brief-watcher: scan the inbox
				// against the state file. If nothing has changed, skip
				// the LLM call entirely — this is the whole point of the
				// brief-watcher, since polling at @every 5m would
				// otherwise burn tokens on every tick. When there ARE
				// pending briefs, render the job's prompt template with
				// the list so the LLM doesn't have to rediscover them
				// (which it has been doing unreliably).
				var processedBrief *briefwatcher.Brief
				if queuedJob.Name == "brief-watcher" {
					pending, err := briefwatcher.Check("/inbox/briefs", "/home/agent/.local/share/busy/briefs-state.json")
					if err != nil {
						log("warn", "briefwatcher", fmt.Sprintf("pre-check failed: %v (proceeding anyway)", err))
					} else if len(pending) == 0 {
						log("info", "briefwatcher", "no pending briefs; skipping LLM call")
						jobsMu.Lock()
						delete(runningJobs, queuedJob.Name)
						jobsMu.Unlock()
						continue
					} else {
						processedBrief = &pending[0]
						names := make([]string, 0, len(pending))
						for _, b := range pending {
							names = append(names, b.Name)
						}
						log("info", "briefwatcher", fmt.Sprintf("%d pending brief(s): %s", len(pending), strings.Join(names, ", ")))
						rendered, rerr := renderBriefWatcherPrompt(queuedJob.Prompt, pending)
						if rerr != nil {
							log("warn", "briefwatcher", fmt.Sprintf("prompt render failed: %v (proceeding with static prompt)", rerr))
						} else {
							queuedJob.Prompt = rendered
						}
					}
				}

				// Run the job
				log("info", "queue", fmt.Sprintf("dequeued job %s", queuedJob.Name))
				web.EmitGlobal("queue", map[string]interface{}{"action": "dequeue", "job": queuedJob})
				runJobFromQueue(ctx, runner, queuedJob)

				// For brief-watcher, the host updates the state based on
				// whether the model actually produced a plan file. This
				// takes the state file out of the model's hands — every
				// earlier failure mode (extra wrapping keys, wrong
				// field names, missing update) is eliminated.
				if processedBrief != nil {
					planPath := filepath.Join("/outbox", processedBrief.Name, "plan.md")
					if _, err := os.Stat(planPath); err == nil {
						if err := briefwatcher.MarkDone("/home/agent/.local/share/busy/briefs-state.json", processedBrief.Name, processedBrief.Mtime, processedBrief.Sha); err != nil {
							log("warn", "briefwatcher", fmt.Sprintf("failed to mark %s done: %v", processedBrief.Name, err))
						} else {
							log("info", "briefwatcher", fmt.Sprintf("marked %s done (plan found)", processedBrief.Name))
						}
					} else {
						log("warn", "briefwatcher", fmt.Sprintf("no plan at %s; %s will retry next cycle", planPath, processedBrief.Name))
					}
				}

				// Mark job as done
				jobsMu.Lock()
				delete(runningJobs, queuedJob.Name)
				jobsMu.Unlock()
			}
		}
	}()

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
	wg.Wait()
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

// renderBriefWatcherPrompt renders the brief-watcher job's prompt
// template with the list of pending briefs. The template uses
// text/template syntax (so `{{.Briefs}}` and friends work); the
// data passed in has a Briefs field (a slice of briefwatcher.Brief
// values with Name, Path, Mtime, Sha).
func renderBriefWatcherPrompt(tmplSrc string, pending []briefwatcher.Brief) (string, error) {
	tmpl, err := template.New("briefwatcher").Parse(tmplSrc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Briefs []briefwatcher.Brief
	}{Briefs: pending}); err != nil {
		return "", err
	}
	return buf.String(), nil
}
