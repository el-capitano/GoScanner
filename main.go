package main

import "log"

func main() {
	if err := runTUI(); err != nil {
		log.Fatal(err)
	}
}
