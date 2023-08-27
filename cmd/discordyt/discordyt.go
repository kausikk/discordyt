package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/joho/godotenv"
	"github.com/kausikk/discordyt/internal"
	"github.com/virtuald/go-paniclog"
)

const VERSION = "v0.3.4-playtest"

const dateFmt = "2006-01-02T15-04-05"

func main() {
	// Read env variables
	fmt.Println("discordyt", VERSION)
	if len(os.Args) < 2 {
		fmt.Println(".env missing")
		os.Exit(1)
	}
	config, err := godotenv.Read(os.Args[1])
	if err != nil {
		fmt.Println(".env parse fail", err)
		os.Exit(1)
	}

	// Open log file
	f, err := os.Create(
		config["LOG_FOLDER"] + "/discordyt-" +
			time.Now().Format(dateFmt) + ".log",
	)
	if err != nil {
		fmt.Println("log open fail", err)
		os.Exit(1)
	}

	// Redirect stderr to log file
	_, err = paniclog.RedirectStderr(f)
	if err != nil {
		fmt.Println("log redirect fail", err)
		os.Exit(1)
	}
	f.Close()

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
