package bridge

import "fmt"

// sprintf is a thin alias so event helpers read cleanly.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
