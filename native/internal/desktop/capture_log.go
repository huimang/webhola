package desktop

import (
	"log"
	"os"
	"path/filepath"
)

const captureLogLimit = 5 << 20

// openCaptureLog appends capture diagnostics to a local file so a packaged
// Windows build, which has no console, can be inspected after a session.
func openCaptureLog() (*log.Logger, func()) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, func() {}
	}
	path := filepath.Join(dir, "Webhola", "logs", "capture.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, func() {}
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= captureLogLimit {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, func() {}
	}
	return log.New(f, "", log.LstdFlags|log.Lmicroseconds), func() { _ = f.Close() }
}
