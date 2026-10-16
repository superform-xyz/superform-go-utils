package mem

type filterer[T any] func(value T) bool

func filter[T any](filters []filterer[T], value T) bool {
	for _, fil := range filters {
		if !fil(value) {
			return false
		}
	}

	return true
}
