package internal

import (
	"context"
	"log"
	"time"
)

const JoinChannelTimeout = 15 * time.Second

func play(gw *Gateway, rootctx context.Context, data InteractionData) {
	ctx, cancel := context.WithTimeout(rootctx, JoinChannelTimeout)
	defer cancel()
	chnl := &data.Data.Options[0].Value
	if *chnl == "leave" {
		chnl = nil
	}
	err := gw.JoinChannel(
		ctx,
		data.GuildId,
		chnl,
	)
	if err != nil {
		log.Println("play err:", err)
	}
}
