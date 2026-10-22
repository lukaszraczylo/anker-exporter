package frame

type signedness uint8

const (
	signDefault signedness = iota
	signYes
	signNo
)

// textKind marks fields reported through Message.Strings.
type textKind uint8

const (
	kindNumber textKind = iota
	kindText
	kindVersion
)

// byteField is a sub-field of a bin (type 04) value at a fixed offset.
type byteField struct {
	name   string
	unit   string
	help   string
	offset int
}

type fieldDef struct {
	name  string
	unit  string
	help  string
	bytes []byteField
	// values is the "values" key of the reference map: 1 reads only the first byte of a var field.
	values int
	factor float64
	tag    byte
	sign   signedness
	kind   textKind
}

// FieldInfo describes one numeric field the decoder can emit.
type FieldInfo struct {
	Name string
	Unit string
	Help string
}

// a1761Fields mirrors _A1761_0405 in anker-solix-api mqttmap.py.
var a1761Fields = []fieldDef{
	{tag: 0xa2, name: "ac_output_timeout_seconds", unit: "seconds", help: "Active AC output auto-off countdown"},
	{tag: 0xa3, name: "dc_output_timeout_seconds", unit: "seconds", help: "Active DC output auto-off countdown"},
	{tag: 0xa4, name: "remaining_time_hours", unit: "hours", help: "Remaining runtime", factor: 0.1, sign: signNo},
	{tag: 0xa5, name: "ac_input_power", unit: "watts", help: "AC charging power to battery"},
	{tag: 0xa6, name: "ac_output_power", unit: "watts", help: "AC outlet output power"},
	{tag: 0xa7, name: "usbc_1_power", unit: "watts", help: "USB-C port 1 output power"},
	{tag: 0xa8, name: "usbc_2_power", unit: "watts", help: "USB-C port 2 output power"},
	{tag: 0xa9, name: "usba_1_power", unit: "watts", help: "USB-A port 1 output power"},
	{tag: 0xaa, name: "usba_2_power", unit: "watts", help: "USB-A port 2 output power"},
	{tag: 0xae, name: "dc_input_power", unit: "watts", help: "DC input power (solar or car charging)"},
	{tag: 0xaf, name: "photovoltaic_power", unit: "watts", help: "Solar input power"},
	{tag: 0xb0, name: "output_power_total", unit: "watts", help: "Combined AC and DC output power"},
	{tag: 0xb3, name: "sw_version", help: "Main firmware version", kind: kindVersion, values: 1},
	{tag: 0xb9, name: "sw_expansion", help: "Expansion firmware version", kind: kindVersion, values: 1},
	{tag: 0xba, name: "sw_controller", help: "Controller firmware version", kind: kindVersion, values: 1},
	{tag: 0xbb, name: "ac_output_status", help: "AC inverter state, 0 off 1 on"},
	{tag: 0xbc, name: "charging_status", help: "Charging source, 0 inactive 1 solar 2 AC 3 both"},
	{tag: 0xbd, name: "temperature", unit: "celsius", help: "Main device temperature", sign: signYes},
	{tag: 0xbe, name: "exp_1_temperature", unit: "celsius", help: "Expansion battery 1 temperature", sign: signYes},
	{tag: 0xc1, name: "main_battery_soc", unit: "percent", help: "Main battery state of charge"},
	{tag: 0xc2, name: "exp_1_soc", unit: "percent", help: "Expansion battery 1 state of charge"},
	{tag: 0xc3, name: "battery_soh", unit: "percent", help: "Main battery state of health"},
	{tag: 0xc4, name: "exp_1_soh", unit: "percent", help: "Expansion battery 1 state of health"},
	{tag: 0xc5, name: "expansion_packs", help: "Number of expansion batteries"},
	{tag: 0xc6, name: "usbc_1_status", help: "USB-C port 1 state, 0 off 1 on"},
	{tag: 0xc7, name: "usbc_2_status", help: "USB-C port 2 state, 0 off 1 on"},
	{tag: 0xc8, name: "usba_1_status", help: "USB-A port 1 state, 0 off 1 on"},
	{tag: 0xc9, name: "usba_2_status", help: "USB-A port 2 state, 0 off 1 on"},
	{tag: 0xcc, name: "dc_12v_1_status", help: "12V car socket state, 0 off 1 on"},
	{tag: 0xd0, name: "device_sn", help: "Device serial number", kind: kindText},
	{tag: 0xd1, name: "ac_input_limit", unit: "watts", help: "Maximum AC charge setting"},
	{tag: 0xd2, name: "device_timeout_minutes", unit: "minutes", help: "Device auto-off timeout, 0 never"},
	{tag: 0xd3, name: "display_timeout_seconds", unit: "seconds", help: "Display timeout"},
	{tag: 0xd7, name: "ac_output_power_switch", help: "AC output switch, 0 disabled 1 enabled"},
	{tag: 0xd8, name: "dc_output_power_switch", help: "DC output switch, 0 disabled 1 enabled"},
	{tag: 0xd9, name: "display_mode", help: "Display brightness, 0 off to 3 high"},
	{tag: 0xda, name: "ac_frequency", unit: "hertz", help: "AC output frequency"},
	{tag: 0xdc, name: "light_mode", help: "LED light mode, 0 off to 3 high, 4 blinking"},
	{tag: 0xdd, name: "temp_unit_fahrenheit", help: "Temperature unit setting, 0 Celsius 1 Fahrenheit"},
	{tag: 0xde, name: "display_switch", help: "Display switch, 0 off 1 on"},
	{tag: 0xe5, name: "ac_fast_charge_switch", help: "Ultrafast charge switch, 0 disabled 1 enabled"},
	{tag: 0xf8, bytes: []byteField{
		{offset: 0, name: "dc_12v_output_mode", help: "12V output mode, 1 normal 2 smart"},
		{offset: 1, name: "ac_output_mode", help: "AC output mode, 1 normal 2 smart"},
	}},
	{tag: 0xfd, name: "exp_1_type", help: "Expansion battery type identifier", kind: kindText},
	{tag: 0xfe, name: "msg_timestamp", unit: "seconds", help: "Device message timestamp (unix)"},
}

var a1761Index = func() map[byte]*fieldDef {
	m := make(map[byte]*fieldDef, len(a1761Fields))
	for i := range a1761Fields {
		m[a1761Fields[i].tag] = &a1761Fields[i]
	}
	return m
}()

// A1761Fields lists the numeric fields in table order.
func A1761Fields() []FieldInfo {
	var out []FieldInfo
	for _, f := range a1761Fields {
		switch {
		case len(f.bytes) > 0:
			for _, b := range f.bytes {
				out = append(out, FieldInfo{Name: b.name, Unit: b.unit, Help: b.help})
			}
		case f.kind == kindNumber:
			out = append(out, FieldInfo{Name: f.name, Unit: f.unit, Help: f.help})
		}
	}
	return out
}
