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
	err := gw.JoinChannel(
		ctx,
		"1076002621875298394",
		"1076002622475075617",
	)
	if err != nil {
		log.Println("play err:", err)
	}
}
