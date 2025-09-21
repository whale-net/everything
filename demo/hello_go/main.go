// Hello Go application
package main

import (
	"fmt"
	"github.com/example/everything/libs/go"
)

func main() {
	message := go_lib.FormatGreeting("world from Bazel - testing change detection")
	fmt.Println(message)
	fmt.Printf("Version: %s\n", go_lib.GetVersion())
}
