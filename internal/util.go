package internal

// Keys returns a slice containing copies of the keys of the given map, in no particular
// order.
func Keys[K comparable, V any](m map[K]V) []K {
	if m == nil {
		return nil
	}
	output := make([]K, 0, len(m))
	for key := range m {
		output = append(output, key)
	}
	return output
}
