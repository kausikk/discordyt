package main

import (
	"context"
	"github.com/joho/godotenv"
	"github.com/kausikk/discord-yt/internal"
	"log"
	"os"
)

const VERSION = "v0.0.0"

func main() {
	log.Println("Version:", VERSION)
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	for {
		gw, err := internal.Connect(context.TODO(), config)
		if err != nil {
			log.Fatal(err)
		}
		err = gw.Listen(context.TODO())
		log.Printf("restart gateway: %v\n", err)
	}
}
