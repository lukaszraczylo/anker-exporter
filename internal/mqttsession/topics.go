package mqttsession

import (
	"strings"

	"github.com/lukaszraczylo/anker-exporter/internal/config"
)

const deviceModel = "A1761"

// TelemetryTopic is the wildcard subscription for all device telemetry.
func TelemetryTopic(appName, sn string) string {
	return "dt/" + appName + "/" + deviceModel + "/" + sn + "/#"
}

// CommandTopic is where the realtime trigger is published.
func CommandTopic(appName, sn string) string {
	return "cmd/" + appName + "/" + deviceModel + "/" + sn + "/req"
}

const serialSegment = 3 // dt|cmd / app / model / serial / ...

// maskTopic hides the serial in a topic so error text can be logged.
func maskTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) > serialSegment {
		parts[serialSegment] = config.MaskSN(parts[serialSegment])
	}
	return strings.Join(parts, "/")
}
