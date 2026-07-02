package util

// GetNumericFloat extracts a float64 value from an interface{} that may contain
// any Go numeric type. Returns the float64 value and true if extraction succeeds,
// or 0 and false otherwise.
// This is necessary because Go type assertions are strict: int cannot be asserted as float64.
func GetNumericFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// MustGetNumericFloat extracts a float64 value from an interface{}.
// Returns defaultValue if extraction fails.
func MustGetNumericFloat(v interface{}, defaultValue float64) float64 {
	if val, ok := GetNumericFloat(v); ok {
		return val
	}
	return defaultValue
}
