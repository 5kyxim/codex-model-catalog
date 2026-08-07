//go:build darwin || linux

package modelcatalog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const shutdownGrace = 3 * time.Second

type childResult struct {
	code int
	err  error
}

func runAppServer(realCodex string, args []string, router *router) int {
	command := exec.Command(realCodex, args...)
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	childInput, err := command.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: create child stdin: %v\n", err)
		return 1
	}
	childOutput, err := command.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: create child stdout: %v\n", err)
		return 1
	}
	if err := command.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: start app-server: %v\n", err)
		return 1
	}

	output := &lockedWriter{w: os.Stdout}
	waitCh := make(chan childResult, 1)
	clientCh := make(chan error, 1)
	serverCh := make(chan error, 1)
	go func() {
		err := command.Wait()
		waitCh <- childExitResult(command, err)
	}()
	go func() {
		clientCh <- pumpClient(os.Stdin, childInput, output, router)
	}()
	go func() {
		serverCh <- pumpServer(childOutput, output, router)
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signalCh)

	var closeInputOnce sync.Once
	closeInput := func() {
		closeInputOnce.Do(func() {
			_ = childInput.Close()
		})
	}

	select {
	case result := <-waitCh:
		return reportChildResult(result)
	case err := <-clientCh:
		closeInput()
		if errors.Is(err, io.EOF) {
			return stopAfterGrace(command, waitCh, false)
		}
		fmt.Fprintf(os.Stderr, "codex-model-catalog: client stream: %v\n", err)
		return stopAfterGrace(command, waitCh, true)
	case err := <-serverCh:
		closeInput()
		if !errors.Is(err, io.EOF) {
			fmt.Fprintf(os.Stderr, "codex-model-catalog: server stream: %v\n", err)
		}
		return stopAfterGrace(command, waitCh, !errors.Is(err, io.EOF))
	case sig := <-signalCh:
		closeInput()
		fmt.Fprintf(os.Stderr, "codex-model-catalog: received %s\n", sig)
		return terminateChild(command, waitCh, false)
	}
}

func stopAfterGrace(command *exec.Cmd, waitCh <-chan childResult, wrapperFailed bool) int {
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case result := <-waitCh:
		if wrapperFailed && result.code == 0 {
			return 1
		}
		return reportChildResult(result)
	case <-timer.C:
		return terminateChild(command, waitCh, wrapperFailed)
	}
}

func terminateChild(command *exec.Cmd, waitCh <-chan childResult, wrapperFailed bool) int {
	_ = signalProcessGroup(command.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case result := <-waitCh:
		if wrapperFailed && result.code == 0 {
			return 1
		}
		return reportChildResult(result)
	case <-timer.C:
		_ = signalProcessGroup(command.Process.Pid, syscall.SIGKILL)
		result := <-waitCh
		if wrapperFailed || result.code == 0 {
			return 1
		}
		return reportChildResult(result)
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func childExitResult(command *exec.Cmd, err error) childResult {
	if command.ProcessState != nil {
		return childResult{code: command.ProcessState.ExitCode(), err: err}
	}
	return childResult{code: 1, err: err}
}

func reportChildResult(result childResult) int {
	if result.err != nil {
		var exitErr *exec.ExitError
		if !errors.As(result.err, &exitErr) {
			fmt.Fprintf(os.Stderr, "codex-model-catalog: app-server exited: %v\n", result.err)
		}
	}
	if result.code < 0 {
		return 1
	}
	return result.code
}
