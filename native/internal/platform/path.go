package platform

import (
	"strings"
)

func CleanTSharkPath(path string) string { return strings.Trim(strings.TrimSpace(path), "\"") }
