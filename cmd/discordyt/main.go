package main

import (
	"context"
	"github.com/joho/godotenv"
	"github.com/kausikk/discordyt/internal"
	"log"
	"os"
	"os/signal"
)

const VERSION = "v0.1.1"

func main() {
	// Read env variables
	log.Println("Version:", VERSION)
	if len(os.Args) < 2 {
		log.Fatal("missing .env file")
	}
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		log.Fatal("unable to parse .env file")
	}

	// Prepare sig int (Ctrl + C) channel
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt)

	// Start gateway
	rootctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw, err := internal.Connect(
		rootctx,
		config["BOT_TOKEN"],
		config["BOT_APP_ID"],
		config["BOT_PUBLIC_KEY"],
		config["SONG_FOLDER"],
	)
	if err != nil {
		log.Fatal("gateway connect failed:", err)
	}

	// Start Listen/Reconnect loop
	go func() {
		for rootctx.Err() == nil {
			err = gw.Listen(rootctx)
			log.Println("restart gateway:", err)
			err = gw.Reconnect(rootctx)
			if err != nil {
				log.Println("gateway connect failed:", err)
				break
			}
		}
	}()

	// Start play loop
	go func() {
		for {
			select {
			case data := <-gw.PlayCmd():
				go play(
					gw, rootctx, data,
					config["BOT_APP_ID"], config["SONG_FOLDER"],
				)
			case <-rootctx.Done():
				return
			}
		}
	}()

	// Start stop loop {
	go func() {
		for {
			select {
			case data := <-gw.StopCmd():
				go stop(gw, rootctx, data)
			case <-rootctx.Done():
				return
			}
		}
	}()

	// Wait for sig int
	<-sigint
	log.Println("captured sigint")
	gw.Close(rootctx)
}
