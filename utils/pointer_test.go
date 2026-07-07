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
