package mqttsession

const deviceModel = "A1761"

// TelemetryTopic is the wildcard subscription for all device telemetry.
func TelemetryTopic(appName, sn string) string {
	return "dt/" + appName + "/" + deviceModel + "/" + sn + "/#"
}

// CommandTopic is where the realtime trigger is published.
func CommandTopic(appName, sn string) string {
	return "cmd/" + appName + "/" + deviceModel + "/" + sn + "/req"
}
