package main

import "testing"

func TestVersionStringIsSet(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
}
