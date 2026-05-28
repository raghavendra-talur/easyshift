package easyshift

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// InitLogging configures logrus to write to logFile, creating any missing
// parent directories. If debug is true, the log level is set to Debug.
func InitLogging(logFile string, debug bool) error {
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	logrus.SetOutput(f)
	return nil
}
