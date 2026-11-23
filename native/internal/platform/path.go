package platform

import (
	"strings"
)

func CleanPath(path string) string { return strings.Trim(strings.TrimSpace(path), "\"") }
