package main

import (
	"os"
)

func main() {
	value, err := os.ReadFile("fixture.txt")
	if err != nil || string(value) != "mock edit complete\n" {
		os.Exit(1)
	}
}
