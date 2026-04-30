// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
)

func initializeServerIdentity(rt *appRuntime) {
	identityInfo := detectServerIdentity(rt)
	rt.serverIDValue = strings.TrimSpace(rt.cfg.ServerID)
	rt.serverMACValue = ""
	if identityInfo != nil {
		applyDetectedIdentity(rt, identityInfo)
	}
	if rt.serverIDValue != "" && rt.cfg.ServerID == "" {
		rt.cfg.ServerID = rt.serverIDValue
	}

	logServerIdentityValues(rt.serverIDValue, rt.serverMACValue)
	logTelegramServerStatus(rt)
	fmt.Println()
}

func detectServerIdentity(rt *appRuntime) *identity.Info {
	info, err := identity.DetectWithContext(rt.ctx, rt.cfg.BaseDir, rt.logger)
	if err != nil {
		logging.Warning("WARNING: Failed to load server identity: %v", err)
	}
	return info
}

func applyDetectedIdentity(rt *appRuntime, info *identity.Info) {
	if info.ServerID != "" {
		rt.serverIDValue = info.ServerID
	}
	if info.PrimaryMAC != "" {
		rt.serverMACValue = info.PrimaryMAC
	}
}

func logTelegramServerStatus(rt *appRuntime) {
	status := "Telegram disabled"
	logTelegramInfo := true
	if rt.cfg.TelegramEnabled {
		status, logTelegramInfo = checkTelegramServerStatus(rt)
	}
	if logTelegramInfo {
		logging.Info("Server Telegram: %s", status)
	}
}

func checkTelegramServerStatus(rt *appRuntime) (string, bool) {
	if !strings.EqualFold(rt.cfg.TelegramBotType, "centralized") {
		return "Personal mode - no remote contact", true
	}

	logging.Debug("Contacting remote Telegram server...")
	logging.Debug("Telegram server URL: %s", rt.cfg.TelegramServerAPIHost)
	status := notify.CheckTelegramRegistration(rt.ctx, rt.cfg.TelegramServerAPIHost, rt.serverIDValue, rt.logger)
	if status.Error != nil {
		logging.Warning("Telegram: %s", status.Message)
		logging.Debug("Telegram connection error: %v", status.Error)
		logging.Skip("Telegram: disabled")
		rt.cfg.TelegramEnabled = false
		return status.Message, false
	}
	logging.Debug("Remote server contacted: Bot token / chat ID verified (handshake)")
	return status.Message, true
}
