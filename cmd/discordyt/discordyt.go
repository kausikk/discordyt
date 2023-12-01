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

const VERSION = "v0.3.8"

const dateFmt = "2006-01-02T15-04-05"

func main() {
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

	f, err := os.Create(
		config["LOG_FOLDER"] + "/discordyt-" +
			time.Now().Format(dateFmt) + ".log",
	)
	if err != nil {
		fmt.Println("log create fail", err)
		os.Exit(1)
	}

	_, err = paniclog.RedirectStderr(f)
	if err != nil {
		fmt.Println("log redirect fail", err)
		os.Exit(1)
	}
	f.Close()

	slog.Info("discordyt", "v", VERSION)

	// Create channel to receive sig int (Ctrl+C)
	sigint := make(chan os.Signal, 2)
	signal.Notify(sigint, os.Interrupt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open gateway
	gw, err := internal.Connect(
		ctx,
		config["BOT_TOKEN"],
		config["BOT_APP_ID"],
	)
	if err != nil {
		slog.Error("gw connect fail", "e", err)
		os.Exit(1)
	}
	defer gw.Close()

	// Start serving gateway
	go func() {
		err = gw.Serve(ctx)
		slog.Error("gw serve fail", "e", err)
		sigint <- os.Interrupt
	}()

	// Start loop to read and handle commands
	guildChnls := make(map[string]chan internal.InteractionData)
	go func() {
		for {
			data := <-gw.Cmd()
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
		}
	}()

	<-sigint
	slog.Info("closing...")
}
