// Hello Go application
package main

import (
	"fmt"

	go_lib "github.com/whale-net/everything/libs/go"
)

func main() {
	message := go_lib.FormatGreeting("world from Bazel BASIL")
	fmt.Println(message)
	fmt.Printf("Version: %s\n", go_lib.GetVersion())
}
