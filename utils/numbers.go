package utils

// BoolToInt64 converts a boolean to an int64
func BoolToInt64(b bool) int64 {
	var i int64 = 0
	if b {
		i = 1
	}
	return i
}
