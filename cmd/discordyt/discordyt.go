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

const VERSION = "v0.3.4-playtest-no-allocs"

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

	f, err := os.Create(
		config["LOG_FOLDER"] + "/discordyt-" +
			time.Now().Format(dateFmt) + ".log",
	)
	if err != nil {
		fmt.Println("log open fail", err)
		os.Exit(1)
	}

	_, err = paniclog.RedirectStderr(f)
	if err != nil {
		fmt.Println("log redirect fail", err)
		os.Exit(1)
	}
	f.Close()

	slog.Info("discordyt", "v", VERSION)

	sigint := make(chan os.Signal, 2)
	signal.Notify(sigint, os.Interrupt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open gateway
	gw, err := internal.Connect(
		ctx,
		config["BOT_TOKEN"],
		config["BOT_APP_ID"],
		config["SONG_FOLDER"],
	)
	if err != nil {
		slog.Error("gw connect fail", "e", err)
		os.Exit(1)
	}

	// Start serving gateway
	go func() {
		err = gw.Serve(ctx)
		slog.Error("gw serve fail", "e", err)
		sigint <- os.Interrupt
	}()

	// Start command loop
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
	gw.Close()
}

func sync(id, token string) error {
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

	data, _ := json.Marshal(syncCmd{
		Name:        "play",
		Type:        1,
		Description: "Play a song from Youtube",
		Options: []syncCmdOption{
			{
				Name:        "query",
				Description: "Keywords to search for video",
				Type:        3,
				Required:    true,
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
	req.Header.Add("Authorization", "Bot "+token)
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

	data, _ = json.Marshal(syncCmd{
		Name:        "stop",
		Type:        1,
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
	req.Header.Add("Authorization", "Bot "+token)
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
