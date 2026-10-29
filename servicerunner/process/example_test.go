package process_test

import (
	"context"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/italypaleale/go-kit/servicerunner"
	"github.com/italypaleale/go-kit/servicerunner/process"
	"github.com/italypaleale/go-kit/signals"
)

func ExampleNewService() {
	// Services that depend on the process wait until it's running
	ready := servicerunner.NewReady()

	svc, err := process.NewService(
		process.WithCommand("my-server", "--port", "8080"),
		process.WithRestart(process.RestartOnFailure),
		process.WithRestartLimit(3),
		process.WithShutdownGraceTimeout(10*time.Second),
		// Send the server SIGTERM if the application dies without stopping it (on Linux and FreeBSD)
		process.WithParentDeathSignal(syscall.SIGTERM),
		process.WithEnv("LOG_LEVEL", "debug"),
		process.WithStdout(os.Stdout),
		process.WithStderr(os.Stderr),
		process.WithOnStart(func(_ context.Context, pid int) {
			log.Printf("Server started with PID %d", pid)
			ready.Signal()
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	dependent := ready.Await(func(ctx context.Context) error {
		// Talk to the server
		<-ctx.Done()
		return nil
	})

	// When the application receives a shutdown signal, the context is canceled and the process is stopped gracefully
	ctx := signals.SignalContext(context.Background())
	err = servicerunner.NewServiceRunner(svc, dependent).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
}
