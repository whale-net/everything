package main

import (
	"bytes"
	"io/ioutil"
	"os"
	"testing"
)

func TestTemporal(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	main()

	w.Close()
	out, _ := ioutil.ReadAll(r)
	os.Stdout = old

	expected := "Hello, from a temporal app!\n"
	if string(out) != expected {
		t.Errorf("Expected %q, got %q", expected, string(out))
	}
}

