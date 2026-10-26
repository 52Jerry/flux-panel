package main

import (
	"fmt"
	"strings"

	"github.com/go-gost/x/service"
)

// Reuse the node identity for accounting without opening a second control
// WebSocket or reporting a partial configuration to the panel reconciler.
func configureStandaloneReporting(local bool, path string) error {
	if path == "" {
		return nil
	}
	if !local {
		return fmt.Errorf("-report-config requires -standalone")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load traffic report configuration: %w", err)
	}
	if strings.TrimSpace(cfg.Addr) == "" || strings.TrimSpace(cfg.Secret) == "" {
		return fmt.Errorf("traffic report configuration requires addr and secret")
	}
	service.SetHTTPReportURL(cfg.Addr, cfg.Secret)
	return nil
}
