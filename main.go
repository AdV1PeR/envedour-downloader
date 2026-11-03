package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"envedour-bot/internal/bot"
	"envedour-bot/internal/config"
	"envedour-bot/internal/executor"
	"envedour-bot/internal/queue"
	"envedour-bot/internal/thermal"
)

func main() {
	armOptimized := flag.Bool("arm-optimized", false, "Enable ARM-specific optimizations")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Ошибка конфигурации:\n%v\n\nПроверьте настройки и попробуйте снова.", err)
	}

	// Initialize thermal monitoring if ARM optimized
	if *armOptimized {
		thermalMonitor := thermal.NewMonitor()
		go thermalMonitor.Start()
		defer thermalMonitor.Stop()
	}

	// Initialize Redis queue
	log.Printf("Connecting to Redis at %s...", cfg.RedisAddr)
	redisQueue, err := queue.NewRedisQueue(cfg.RedisAddr)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer redisQueue.Close()
	log.Printf("✓ Redis connected")

	// Initialize executor
	exec := executor.NewExecutor(cfg, *armOptimized)
	// Workers track in-flight jobs in Redis for crash recovery.
	exec.SetRedisClient(redisQueue.GetClient())
	defer exec.Close()

	// Initialize bot
	apiURL := "Telegram API"
	if cfg.LocalAPIURL != "" {
		apiURL = cfg.LocalAPIURL
	}
	log.Printf("Connecting to %s...", apiURL)
	envedourBot, err := bot.NewBot(cfg, redisQueue, exec)
	if err != nil {
		log.Fatalf("Failed to initialize bot: %v", err)
	}
	log.Printf("✓ Bot initialized")

	// Recover jobs that were in flight when the previous run died. Must run
	// before the workers start so recovered jobs are not raced by new ones.
	if recovered := exec.RecoverInflight(redisQueue); recovered > 0 {
		log.Printf("✓ Recovered %d in-flight job(s)", recovered)
	} else {
		log.Printf("✓ No in-flight jobs to recover")
	}

	// Two lifecycles: the bot stops consuming updates first, workers get a
	// grace period to finish the current job before they are force-cancelled
	// (interrupted jobs are recovered on the next start via the inflight
	// registry).
	botCtx, botCancel := context.WithCancel(context.Background())
	defer botCancel()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	drain := make(chan struct{})

	var workersWG sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			exec.Worker(workerCtx, redisQueue, drain)
		}()
	}

	// Start bot
	go envedourBot.Start(botCtx)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutdown: stop accepting updates, finishing current jobs…")

	// Phase 1: stop taking new work and stop polling Telegram.
	close(drain)
	botCancel()
	envedourBot.Stop()

	// Phase 2: give the workers time to finish the current job.
	done := make(chan struct{})
	go func() {
		workersWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Println("Shutdown: all workers finished cleanly")
	case <-time.After(30 * time.Second):
		log.Println("Shutdown: grace period exceeded, cancelling workers (jobs will be recovered on the next start)")
		workerCancel()
		<-done
	}
}
