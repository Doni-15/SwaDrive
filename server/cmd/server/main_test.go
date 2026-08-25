package main

import "testing"

func TestMaximumHTTPHeaderBytesIs64KiB(t *testing.T) {
	if maximumHTTPHeaderBytes != 64<<10 {
		t.Fatalf("maximumHTTPHeaderBytes = %d; want %d", maximumHTTPHeaderBytes, 64<<10)
	}
}
