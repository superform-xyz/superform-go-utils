package utils

import "testing"

func TestPtr(t *testing.T) {
	value := Ptr("superform")
	if value == nil {
		t.Fatal("Ptr returned nil")
	}
	if *value != "superform" {
		t.Fatalf("Ptr returned %q, want %q", *value, "superform")
	}
}

func TestDerefAndValueOr(t *testing.T) {
	var nilInt *int
	if Deref(nilInt) != 0 || Deref(Ptr(7)) != 7 {
		t.Fatal("Deref must return the zero value for nil and the pointee otherwise")
	}
	if ValueOr(nilInt, 3) != 3 || ValueOr(Ptr(7), 3) != 7 {
		t.Fatal("ValueOr must return the fallback for nil and the pointee otherwise")
	}
}
