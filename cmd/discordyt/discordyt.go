package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/joho/godotenv"
	"github.com/kausikk/discordyt/internal"
	"github.com/kausikk/discordyt/internal/splitlog"
)

const VERSION = "v0.3.3"

func main() {
	// Read env variables
	fmt.Println("discordyt", VERSION)
	if len(os.Args) < 2 {
		slog.Error(".env missing")
		os.Exit(1)
	}
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		slog.Error(".env parse fail", "e", err)
		os.Exit(1)
	}

	// Create log file. logger also redirects
	// stderr to the currently open log file.
	logger, err := splitlog.Open(config["LOG_FOLDER"])
	if err != nil {
		slog.Error("logger open fail", "e", err)
		os.Exit(1)
	}
	defer logger.Close()
	slogger := slog.New(slog.NewTextHandler(logger, nil))
	slog.SetDefault(slogger)
	slog.Info("discordyt", "v", VERSION)

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
		slog.Error("gw connect fail", "e", err)
		os.Exit(1)
	}

	// Start Listen/Reconnect loop
	go func() {
		for {
			select {
			default:
				err = gw.Serve(ctx)
				slog.Error("gw serve fail", "e", err)
				err = gw.Reconnect(ctx)
				if err != nil {
					slog.Error("gw reconnect fail", "e", err)
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
	slog.Info("closing...")
	gw.Close()
}
