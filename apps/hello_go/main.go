// Hello Go application
package main

import (
	"fmt"
	"github.com/example/everything/libs/common_go"
)

func main() {
	message := common.FormatGreeting("world from Bazel")
	fmt.Println(message)
	fmt.Printf("Version: %s\n", common.GetVersion())
}
