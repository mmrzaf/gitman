package main

import "fmt"

func runVersion(_ []string) error {
	fmt.Println("gitman " + version)
	return nil
}
