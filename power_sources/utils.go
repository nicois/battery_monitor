package power_sources

import (
	"log"

	"go.uber.org/zap"
)

var logger *zap.Logger

// init is automatically run when the package is loaded, initialising
// any required globals
func init() {
	_logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	logger = _logger
}
