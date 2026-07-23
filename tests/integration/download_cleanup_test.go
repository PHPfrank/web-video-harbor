package integration_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForNoDownloadStagingWaitsForDelayedWorkerCleanup(t *testing.T) {
	downloadDir := t.TempDir()
	stagingDir := filepath.Join(downloadDir, ".web-video-controlled")
	if err := os.Mkdir(stagingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "download.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.RemoveAll(stagingDir)
		close(removed)
	}()

	if err := waitForNoDownloadStaging(downloadDir, time.Second); err != nil {
		t.Fatal(err)
	}
	<-removed
}
