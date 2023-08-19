package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/joho/godotenv"
	"github.com/kausikk/discordyt/internal"
)

const VERSION = "v0.2.1"

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw, err := internal.Connect(
		ctx,
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
		for {
			select {
			default:
				err = gw.Serve(ctx)
				log.Println("restart gateway:", err)
				err = gw.Reconnect(ctx)
				if err != nil {
					log.Println("gateway connect failed:", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start play loop
	go func() {
		for {
			select {
			case data := <-gw.PlayCmd():
				go play(
					gw, ctx, data,
					config["BOT_APP_ID"],
					config["YT_API_KEY"],
					config["SONG_FOLDER"],
				)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start stop loop {
	go func() {
		for {
			select {
			case data := <-gw.StopCmd():
				go stop(gw, ctx, data)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait for sig int
	<-sigint
	log.Println("closing...")
	gw.Close()
}
