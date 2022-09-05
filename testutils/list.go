package testutils

import "math/rand"

// Moves a random element in `before`. Returns a new array with the element moved, the item moved,
// and which index positions it was moved from/to.
func MoveRandomElement(before []string) (after []string, item string, fromIndex, toIndex int) {
	// randomly choose an element to move
	fromIndex = rand.Intn(len(before))
	item = before[fromIndex]
	// randomly choose a destination location
	toIndex = rand.Intn(len(before))
	// create the after array
	for j := range before {
		if j == fromIndex {
			if j == toIndex {
				after = append(after, before[j]) // no-op move
			}
			continue // skip over before item
		}
		if j == toIndex {
			if j > fromIndex {
				// we've skipped item already, need to make up for it now
				after = append(after, before[toIndex])
				after = append(after, item)
			} else {
				// we will skip over item, need to make up for it now
				after = append(after, item)
				after = append(after, before[toIndex])
			}
			continue
		}
		after = append(after, before[j])
	}
	return
}
