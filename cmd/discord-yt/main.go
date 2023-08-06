package main

import (
	"os"
	"log"
	"context"
	"github.com/kausikk/discord-yt/internal"
	"github.com/joho/godotenv"
)

const VERSION = "v0.0.0"

func main() {
	log.Println("Version:", VERSION)
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	for {
		err = internal.Run(context.Background(), config)
		log.Println("restart gateway, err:", err)
	}
}
