package main

import (
	"log"

	"github.com/nats-io/nats.go"
)

func main() {
	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	err = nc.Publish(
		"hello",
		[]byte("Salut din client!"),
	)

	if err != nil {
		log.Fatal(err)
	}

	err = nc.Flush()
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Mesaj trimis!")
}
