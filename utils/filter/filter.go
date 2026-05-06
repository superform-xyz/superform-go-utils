package filter

// Filter accepts an array of any type and filters
// the list of elements according to the boolean value of the predicate.
func Filter[T any](ss []T, test func(T) bool) (ret []T) {
	for _, s := range ss {
		if test(s) {
			ret = append(ret, s)
		}
	}
	return
}

// Contains checks if a slice contains a specific element
func Contains[T comparable](ss []T, s T) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// IsEqual checks if two slices are equal
func IsEqual[T comparable](a, b []T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(a) != len(b) {
		return false
	}

	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

// Unique returns a slice with unique elements
func Unique[T comparable](ss []T) []T {
	set := make(map[T]bool)
	ret := []T{}
	for _, s := range ss {
		if _, ok := set[s]; ok {
			continue
		}
		set[s] = true
		ret = append(ret, s)
	}
	return ret
}

// IsExactlyOneBooleanTrue checks if exactly one boolean in the array is true if at least one boolean is true
// returns true if there is no true or if there is exactly one true
// returns false if there are multiple true
func IsExactlyOneBooleanTrue(boolAry []bool) bool {
	areAnyTrue := false
	areTwoTrue := false
	for i := 0; (!areTwoTrue) && (i < len(boolAry)); i++ {
		areTwoTrue = (areAnyTrue && boolAry[i])
		areAnyTrue = areAnyTrue || boolAry[i]
	}

	if areAnyTrue {
		return !areTwoTrue
	}
	return true
}
