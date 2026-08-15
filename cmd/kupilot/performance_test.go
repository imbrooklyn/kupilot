package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var cliStartupBenchmarkSink []byte

// BenchmarkCLIProcessStartupV1 measures an already-built native binary. Run
// with -benchtime=1x and repeated -count samples so each result is one process.
func BenchmarkCLIProcessStartupV1(b *testing.B) {
	binary := filepath.Join("..", "..", "bin", "kupilot")
	if override := os.Getenv("KUPILOT_PERF_BINARY"); override != "" {
		binary = override
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		b.Fatal("native performance binary is unavailable; run the performance build first")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		b.Fatal("native performance binary is not executable")
	}

	workloads := []struct {
		name       string
		argument   string
		wantOutput string
	}{
		{name: "Version", argument: "version", wantOutput: "kupilot version="},
		{name: "Help", argument: "help", wantOutput: "Usage:"},
	}
	for _, workload := range workloads {
		b.Run(workload.name, func(b *testing.B) {
			root := b.TempDir()
			environment := []string{
				"HOME=" + root,
				"PATH=/usr/bin:/bin",
				"TMPDIR=" + root,
				"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
				"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
				"XDG_STATE_HOME=" + filepath.Join(root, "state"),
				"KUBECONFIG=" + filepath.Join(root, "absent-kubeconfig"),
			}
			b.ResetTimer()
			for range b.N {
				var output bytes.Buffer
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				command := exec.CommandContext(ctx, binary, workload.argument)
				command.Env = environment
				command.Stdout = &output
				command.Stderr = &output
				err := command.Run()
				cancel()
				if err != nil {
					b.Fatalf("%s process failed safely", workload.name)
				}
				if !strings.Contains(output.String(), workload.wantOutput) {
					b.Fatalf("%s process returned unexpected bounded output", workload.name)
				}
				cliStartupBenchmarkSink = append(cliStartupBenchmarkSink[:0], output.Bytes()...)
			}
			b.StopTimer()
			entries, err := os.ReadDir(root)
			if err != nil {
				b.Fatal("isolated startup directory could not be inspected")
			}
			if len(entries) != 0 {
				b.Fatalf("%s startup created %d isolated filesystem entries, want zero", workload.name, len(entries))
			}
		})
	}
}
