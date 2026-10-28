package main

import (
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
)

func main() {
	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	_, err = nc.Subscribe("hello", func(msg *nats.Msg) {
		fmt.Println("Am primit:", string(msg.Data))
	})

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Serverul ascultă pe subject: hello")

	select {}
}
