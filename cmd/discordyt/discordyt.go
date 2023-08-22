package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/joho/godotenv"
	"github.com/kausikk/discordyt/internal"
)

const VERSION = "v0.3.2"

func main() {
	// Read env variables
	fmt.Println("Version:", VERSION)
	if len(os.Args) < 2 {
		log.Fatal("missing .env file")
	}
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		log.Fatal("unable to parse .env file")
	}

	// Create log file
	f, err := os.OpenFile(
		config["LOG_FILE"],
		os.O_WRONLY | os.O_CREATE | os.O_APPEND,
		0644,
	)
	if err != nil {
		log.Fatal("failed to open log file")
	}
	defer f.Close()
	log.SetOutput(f)
	log.Println("Version:", VERSION)

	// Prepare sig int (Ctrl + C) channel
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start gateway
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

	// Start command loop
	guildChnls := make(map[string]chan internal.InteractionData)
	go func() {
		for {
			select {
			case data := <-gw.Cmd():
				chnl, ok := guildChnls[data.GuildId]
				if !ok {
					chnl = make(chan internal.InteractionData)
					guildChnls[data.GuildId] = chnl
					go guildCmdHandler(
						gw, ctx, chnl, data.GuildId,
						config["BOT_APP_ID"],
						config["YT_API_KEY"],
						config["SONG_FOLDER"],
					)
				}
				chnl <- data
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
