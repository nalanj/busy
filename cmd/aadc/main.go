package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/nalanj/aadc/internal/agent"
	"github.com/nalanj/aadc/internal/config"
	"github.com/nalanj/aadc/internal/queue"
	"github.com/nalanj/aadc/internal/scheduler"
	"github.com/nalanj/aadc/internal/web"
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

	runner, err := agent.New(&cfg.Agent, agent.StandardTools())
	if err != nil {
		log("error", "agent", fmt.Sprintf("failed to create runner: %v", err))
		os.Exit(1)
	}
	defer runner.Close()

	// Create job queue
	jobQueue, err := queue.New(cfg.Agent.StateDir, cfg.Agent.Name)
	if err != nil {
		log("error", "queue", fmt.Sprintf("failed to create job queue: %v", err))
		os.Exit(1)
	}

	// Start web server if configured
	if cfg.Agent.ListenAddr != "" {
		server := web.New(cfg.Agent.ListenAddr, runner.Store(), jobQueue, cfg.Agent.Name)
		runner.SetEmitter(server.SSEHub())
		web.SetGlobalEmitter(server.SSEHub())

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

	sched := scheduler.New()

	for _, job := range cfg.Jobs {
		job := job // capture range variable
		log("info", "scheduler", fmt.Sprintf("registering job %s with schedule %s", job.Name, job.Schedule))

		schedule := job.Schedule
		if schedule == "@once" || schedule == "@session-start" {
			// Run immediately by enqueuing
			enqJob := queue.Job{
				Name:       job.Name,
				Prompt:     job.Prompt,
				Trigger:    schedule,
				EnqueuedAt: time.Now(),
			}
			jobQueue.Enqueue(enqJob)
			web.EmitGlobal("queue", map[string]interface{}{"action": "enqueue", "job": enqJob})
			log("info", "scheduler", fmt.Sprintf("job %s queued for immediate execution (%s)", job.Name, schedule))
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
