package webhook

import "os"

// makeDir wraps os.MkdirAll. Lives in its own file because both secret.go
// and queue.go need it but I'd rather not import "os" twice into one file
// that's mostly about something else.
func makeDir(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}
