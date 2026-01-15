package logging

import (
	"fmt"
	"time"
)

// DebugStart logs a standardized debug start line and returns a function that
// logs the end status (ok/error) with duration.
func DebugStart(logger *Logger, operation string, format string, args ...interface{}) func(error) {
	if logger == nil {
		return func(error) {}
	}

	detail := ""
	if format != "" {
		detail = fmt.Sprintf(format, args...)
	}
	if detail != "" {
		logger.Debug("Start %s: %s", operation, detail)
	} else {
		logger.Debug("Start %s", operation)
	}

	started := time.Now()
	return func(err error) {
		if err != nil {
			logger.Debug("End %s (error=%v, duration=%s)", operation, err, time.Since(started))
			return
		}
		logger.Debug("End %s (ok, duration=%s)", operation, time.Since(started))
	}
}

// DebugStep logs a standardized debug progress line for an operation.
func DebugStep(logger *Logger, operation string, format string, args ...interface{}) {
	if logger == nil {
		return
	}
	message := fmt.Sprintf(format, args...)
	if operation == "" {
		logger.Debug("%s", message)
		return
	}
	logger.Debug("%s: %s", operation, message)
}
