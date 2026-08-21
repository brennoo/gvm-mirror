// Command mirrorimages copies every image its lock file's previous run
// recorded into a registry this project controls, using Skopeo, records
// what it did back into that lock file, and optionally rewrites a Docker
// Compose file's pins to match. See mirror-images.sh, the thin wrapper
// around this command.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/brennoo/gvm-mirror/internal/mirror"
)

func main() {
	if err := run(context.Background(), os.Args[1:], mirror.Exec, time.Now, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mirrorimages:", err)
		os.Exit(1)
	}
}

func checkSkopeoAvailable() error {
	if _, err := exec.LookPath("skopeo"); err != nil {
		return fmt.Errorf("skopeo is required to mirror images without pulling: %w", err)
	}
	return nil
}
