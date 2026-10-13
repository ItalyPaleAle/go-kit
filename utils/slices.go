package utils

// StringSlicesMatch returns true if two slices of strings contain the same elements (regardless of the order)
func StringSlicesMatch(listA []string, listB []string) bool {
	aLen := len(listA)
	bLen := len(listB)

	if aLen != bLen {
		return false
	}

	visited := make([]bool, bLen)
	for i := range aLen {
		found := false
		element := listA[i]
		for j := range bLen {
			if visited[j] {
				continue
			}

			if element == listB[j] {
				visited[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
