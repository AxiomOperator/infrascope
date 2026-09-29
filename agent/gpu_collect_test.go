//go:build testing && !windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLongRunningCollector writes a fake collector that records its PID, prints
// output, then keeps running like `nvidia-smi -l` would.
func writeLongRunningCollector(t *testing.T, output string) (script, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "fake-collector")
	pidFile = filepath.Join(dir, "pid")
	body := "#!/bin/sh\necho $$ > " + pidFile + "\n" + output + "\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script, pidFile
}

func readCollectorPid(t *testing.T, pidFile string) int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	return pid
}

// processGone reports whether pid no longer exists (killed and reaped).
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestGpuCollectorKillsChildOnInvalidData(t *testing.T) {
	script, pidFile := writeLongRunningCollector(t, "echo invalid")
	c := &gpuCollector{
		name:    script,
		bufSize: 1024,
		parse:   func([]byte) bool { return false },
	}

	done := make(chan error, 1)
	go func() { done <- c.collect() }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, errNoValidData)
	case <-time.After(10 * time.Second):
		t.Fatal("collect did not return after invalid data")
	}
	assert.True(t, processGone(readCollectorPid(t, pidFile)), "collector child must be killed and reaped")
}

func TestGpuCollectorKillsChildOnScannerError(t *testing.T) {
	// A single line longer than the scanner's max token size triggers bufio.ErrTooLong.
	script, pidFile := writeLongRunningCollector(t, "head -c 100000 /dev/zero | tr '\\0' a")
	c := &gpuCollector{
		name:    script,
		bufSize: 1024,
		parse:   func([]byte) bool { return true },
	}

	done := make(chan error, 1)
	go func() { done <- c.collect() }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scanner error")
	case <-time.After(10 * time.Second):
		t.Fatal("collect did not return after scanner error")
	}
	assert.True(t, processGone(readCollectorPid(t, pidFile)), "collector child must be killed and reaped")
}
