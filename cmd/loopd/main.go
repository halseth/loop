package main

import (
	"fmt"

	"github.com/lightninglabs/loop/loopd"
)

func main() {
	err := loopd.Start(nil)
	if err != nil {
		fmt.Println(err)
	}
}
