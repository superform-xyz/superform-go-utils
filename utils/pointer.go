package utils

// Ptr returns a pointer to value.
func Ptr[T any](value T) *T {
	return &value
}

// Deref returns *p, or the zero value when p is nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// ValueOr returns *p, or fallback when p is nil.
func ValueOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
