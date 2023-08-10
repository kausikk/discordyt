package internal

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

const DiscordApi = "https://discord.com/api"
const JoinChannelTimeout = 10 * time.Second

type InteractionRespType int64

const (
	Pong InteractionRespType = iota + 1
	_
	_
	ChannelMessageWithSource
	DeferredChannelMessageWithSource
	DeferredUpdateMsg
	UpdateMessage
	ApplicationCommandAutocompleteResult
	Modal
)

func play(gw *Gateway, rootctx context.Context, data InteractionData) {
	// Check if user is in a channel
	chnl, ok := gw.UserOccupancy.Get(data.GuildId + data.Member.User.Id)
	if !ok || chnl == "" {
		postInteractionResp(
			data.Id, data.Token,
			"User is not in a channel (try re-joining)",
			ChannelMessageWithSource,
		)
		return
	}

	// Try to join channel
	joinChnl := &data.Data.Options[0].Value
	if *joinChnl == "leave" {
		joinChnl = nil
	}
	ctx, cancel := context.WithTimeout(rootctx, JoinChannelTimeout)
	defer cancel()
	err := gw.JoinChannel(ctx, data.GuildId, joinChnl)

	var msg string
	if err != nil {
		log.Println("play error:", err)
		msg = "Failed to join channel"
	} else {
		msg = "Joined channel"
	}
	postInteractionResp(
		data.Id, data.Token, msg,
		ChannelMessageWithSource,
	)
}

func postInteractionResp(id, token, msg string, intType InteractionRespType) error {
	resp, err := http.Post(
		fmt.Sprintf(
			"%s/interactions/%s/%s/callback",
			DiscordApi, id, token,
		),
		"application/json",
		bytes.NewBufferString(fmt.Sprintf(
			`{"type":%d,"data":{"content":"%s"}}`,
			intType, msg,
		)),
	)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
