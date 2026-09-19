package utils

type signedInteger interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// PositiveOr returns value when it is positive and fallback otherwise
func PositiveOr[T signedInteger](value T, fallback T) T {
	if value > 0 {
		return value
	}
	return fallback
}

// BoolToInt64 converts a boolean to an int64
func BoolToInt64(b bool) int64 {
	var i int64 = 0
	if b {
		i = 1
	}
	return i
}
