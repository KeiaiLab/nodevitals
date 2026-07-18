package collector

// xidClass is the classification of one NVIDIA Xid error code: how severe it
// is, the event condition it maps to, and a short human-readable summary.
type xidClass struct {
	Severity, Condition, Description string
}

// defaultXidClass is returned for any Xid code not present in xidClasses — a
// conservative "warning" default (design doc §3) rather than silently
// treating an unrecognized code as benign.
var defaultXidClass = xidClass{
	Severity:    "warning",
	Condition:   "gpu_xid_error",
	Description: "unknown/unclassified XID",
}

// xidClasses is the Xid → classification lookup table (design doc §3, itself
// M2 §4.3's table). Every entry's Condition is "gpu_xid_error" — Xid
// classification only ever changes Severity/Description, never the
// condition a downstream rule/alert would match on.
var xidClasses = map[uint64]xidClass{
	// info (benign) — application-triggered or otherwise expected; no
	// operator action implied.
	13: {Severity: "info", Condition: "gpu_xid_error", Description: "Graphics engine exception"},
	31: {Severity: "info", Condition: "gpu_xid_error", Description: "GPU memory page fault"},
	43: {Severity: "info", Condition: "gpu_xid_error", Description: "GPU stopped processing (usually application-triggered, GPU stays healthy)"},

	// warning — degraded but still serving; worth watching.
	63: {Severity: "warning", Condition: "gpu_xid_error", Description: "ECC page retirement or row-remap recording event"},
	74: {Severity: "warning", Condition: "gpu_xid_error", Description: "NVLink error"},
	92: {Severity: "warning", Condition: "gpu_xid_error", Description: "High single-bit ECC error rate"},
	94: {Severity: "warning", Condition: "gpu_xid_error", Description: "Contained ECC error"},

	// critical — GPU likely unusable/unreliable until reset or repair.
	48:  {Severity: "critical", Condition: "gpu_xid_error", Description: "Double-bit ECC error"},
	64:  {Severity: "critical", Condition: "gpu_xid_error", Description: "ECC page retirement or row-remap recording failure"},
	79:  {Severity: "critical", Condition: "gpu_xid_error", Description: "GPU has fallen off the bus"},
	95:  {Severity: "critical", Condition: "gpu_xid_error", Description: "Uncontained ECC error"},
	119: {Severity: "critical", Condition: "gpu_xid_error", Description: "GSP RPC timeout"},
	120: {Severity: "critical", Condition: "gpu_xid_error", Description: "GSP error"},
}

// classifyXid returns the classification for an NVIDIA Xid error code. Codes
// not present in the table conservatively default to "warning" rather than
// being silently treated as benign.
func classifyXid(xid uint64) xidClass {
	if c, ok := xidClasses[xid]; ok {
		return c
	}
	return defaultXidClass
}
