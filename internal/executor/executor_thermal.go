//go:build arm64
// +build arm64

package executor

import (
	"envedour-bot/internal/thermal"
)

func init() {
	// Initialize thermal monitor on ARM64
	thermalMonitorImpl = thermal.NewMonitor()
}
