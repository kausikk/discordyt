package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	// Sync global discord bot commands
	// if "sync" flag present
	if len(os.Args) > 2 {
		if os.Args[2] == "sync" {
			fmt.Println("syncing commands...")
			err := sync(config["BOT_APP_ID"], config["BOT_TOKEN"])
			if err != nil {
				fmt.Println("sync commands fail", err)
			} else {
				fmt.Println("success")
			}
			return
		}
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

func sync(id, token string) error {
	// Define command structure
	type syncCmdOption struct {
		Name        string `json:"name"`
		Type        int    `json:"type"`
		Description string `json:"description"`
		Required    bool   `json:"required"`
	}
	type syncCmd struct {
		Name        string          `json:"name"`
		Type        int             `json:"type"`
		Description string          `json:"description"`
		Options     []syncCmdOption `json:"options"`
	}
	client := &http.Client{}

	// Sync play command
	data, _ := json.Marshal(syncCmd{
		Name: "play",
		Type: 1,
		Description: "Play a song from Youtube",
		Options: []syncCmdOption{
			{
				Name: "query",
				Description: "Keywords to search for video",
				Type: 3,
				Required: true,
			},
		},
	})
	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf(
			"%s/applications/%s/commands",
			discordApi, id,
		),
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", "Bot " + token)
	req.Header.Add("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf(
			"post fail, status: %d",
			resp.StatusCode,
		)
	}

	// Sync stop command
	data, _ = json.Marshal(syncCmd{
		Name: "stop",
		Type: 1,
		Description: "Stop song and leave channel",
	})
	req, err = http.NewRequest(
		"POST",
		fmt.Sprintf(
			"%s/applications/%s/commands",
			discordApi, id,
		),
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", "Bot " + token)
	req.Header.Add("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf(
			"post fail, status: %d",
			resp.StatusCode,
		)
	}

	return nil
}
