package main

import (
	"context"
	"github.com/joho/godotenv"
	"github.com/kausikk/discord-yt/internal"
	"log"
	"os"
	"os/signal"
)

const VERSION = "v0.0.0"

func main() {
	log.Println("Version:", VERSION)
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw, err := internal.Connect(
		context.TODO(),
		config["BOT_TOKEN"],
		config["BOT_APP_ID"],
		config["BOT_PUBLIC_KEY"],
	)
	if err != nil {
		log.Fatal("gateway connect failed:", err)
	}
	go func() {
		for ctx.Err() == nil {
			err = gw.Listen(ctx)
			log.Println("restart gateway:", err)
			err = gw.Reconnect(ctx)
			if err != nil {
				log.Println("gateway connect failed:", err)
				break
			}
		}
	}()
	<-sigint
	log.Println("captured sigint")
	gw.Close(ctx)
	cancel()
}
