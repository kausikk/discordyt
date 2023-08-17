package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/kausikk/discordyt/internal"
)

const discordApi = "https://discord.com/api"
const ytubeSearch = "https://www.googleapis.com/youtube/v3/search?part=snippet&maxResults=3&type=video&safeSearch=none"

type interactionRespType int64

const (
	pong interactionRespType = iota + 1
	_
	_
	channelMessageWithSource
	deferredChannelMessageWithSource
	deferredUpdateMsg
	updateMessage
	applicationCommandAutocompleteResult
	modal
)

type ytSearchList struct {
	Items []ytSearch `json:"items"`
}
type ytSearch struct {
	Id      ytSearchId      `json:"id"`
	Snippet ytSearchSnippet `json:"snippet"`
}
type ytSearchId struct {
	VideoId string `json:"videoId"`
}
type ytSearchSnippet struct {
	Title string `json:"title"`
}

type interactionPost struct {
	Type interactionRespType `json:"type"`
	Data interactionContent  `json:"data"`
}
type interactionContent struct {
	Content string `json:"content"`
}

func play(gw *internal.Gateway, rootctx context.Context, data internal.InteractionData, botAppId, ytApiKey, songFolder string) {
	// Check if user is in a channel
	chnl, ok := gw.GetUserChannel(data.GuildId, data.Member.User.Id)
	if !ok || chnl == "" {
		postResp(
			data.Id, data.Token,
			"User is not in a channel (try re-joining)",
			channelMessageWithSource,
		)
		return
	}

	// Respond to interaction (must be done quickly)
	postResp(
		data.Id, data.Token, "Finding song...",
		deferredChannelMessageWithSource,
	)

	// Search youtube for most relevant video
	query := data.Data.Options[0].Value
	results, err := func() (*ytSearchList, error) {
		req, err := http.NewRequest("GET", ytubeSearch, nil)
		if err != nil {
			return nil, err
		}
		params := req.URL.Query()
		params.Set("q", query)
		params.Set("key", ytApiKey)
		req.URL.RawQuery = params.Encode()

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		results := ytSearchList{}
		err = json.Unmarshal(body, &results)
		if err != nil {
			return nil, err
		}
		return &results, nil
	}()
	if err != nil {
		log.Println("ytapi err:", err)
		patchResp(
			botAppId, data.Token,
			"Failed to find '"+query+"'",
		)
		return
	}

	// Extract id from top result
	songId := results.Items[0].Id.VideoId
	songPath := songFolder + "/" + songId + ".opus"

	// Check if file already exists
	// Otherwise download it
	if !checkExists(songPath) {
		log.Println("downloading", songId)

		// Download from youtube with yt-dlp
		err := ytdlpCmd(songFolder, songId)
		if err != nil {
			log.Println("yt-dlp err:", err)
			msg := "No suitable format available for " + songId
			if err.Error() != "format unavailable" {
				msg = "Failed to download " + songId
			}
			patchResp(
				botAppId, data.Token, msg,
			)
			return
		}

		// Convert to opus file
		err = ffmpegCmd(songFolder + "/" + songId)
		if err != nil {
			log.Println("ffmpeg err:", err)
			patchResp(
				botAppId, data.Token,
				"Failed to convert "+songId,
			)
			return
		}

		// Delete .webm file
		os.Remove(songFolder + "/" + songId + ".webm")

		// Check that file exists
		if !checkExists(songPath) {
			patchResp(
				botAppId, data.Token,
				"Failed to download "+songId,
			)
			return
		}

		log.Println("downloaded", songId)
	} else {
		log.Println("already have", songId)
	}

	// Try to join channel
	joinChnl := &chnl
	if *joinChnl == "" {
		joinChnl = nil
	}
	err = gw.JoinChannel(rootctx, data.GuildId, joinChnl)

	if err != nil {
		log.Println("play err:", err)
		var msg string
		if err.Error() == "could not lock" {
			msg = "Another song is playing"
		} else {
			msg = "Could not join channel"
		}
		patchResp(botAppId, data.Token, msg)
		return
	}

	title := html.UnescapeString(results.Items[0].Snippet.Title)
	patchResp(
		botAppId, data.Token,
		"Playing "+title,
	)

	err = gw.PlayAudio(
		rootctx,
		data.GuildId,
		songPath,
	)
	if err != nil {
		log.Println("play error:", songId, err)
		return
	}
	log.Println("done playing", songId)
}

func stop(gw *internal.Gateway, rootctx context.Context, data internal.InteractionData) {
	log.Println("stopping", data.GuildId)
	gw.StopAudio(rootctx, data.GuildId)
	postResp(
		data.Id, data.Token, "Stopped",
		channelMessageWithSource,
	)
}

func postResp(id, token, msg string, intType interactionRespType) error {
	data, _ := json.Marshal(interactionPost{
		intType, interactionContent{msg},
	})
	body := bytes.NewReader(data)
	resp, err := http.Post(
		fmt.Sprintf(
			"%s/interactions/%s/%s/callback",
			discordApi, id, token,
		),
		"application/json",
		body,
	)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func patchResp(id, token, msg string) error {
	data, _ := json.Marshal(interactionContent{msg})
	body := bytes.NewReader(data)
	req, err := http.NewRequest(
		"PATCH",
		fmt.Sprintf(
			"%s/webhooks/%s/%s/messages/@original",
			discordApi, id, token,
		),
		body,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func ytdlpCmd(songFolder, id string) error {
	outFmt := fmt.Sprintf("%s/%%(id)s.%%(ext)s", songFolder)
	cmd := exec.Command("conda", "run", "-n", "yt-bot-env", "yt-dlp", "-f", "ba[acodec=opus][asr=48K][ext=webm][audio_channels=2]", "-o", outFmt, id)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()
	strErr := errOut.String()
	if strings.Contains(strErr, "Requested format is not available") {
		return errors.New("format unavailable")
	} else if err != nil || strings.Contains(strErr, "ERROR") {
		return errors.New(strErr)
	}
	return nil
}

func ffmpegCmd(songPathNoExt string) error {
	cmd := exec.Command("ffmpeg", "-i", songPathNoExt+".webm", "-map_metadata", "-1", "-vn", "-c:a", "copy", "-f", "opus", "-ar", "48000", "-ac", "2", songPathNoExt+".opus")
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()
	strErr := errOut.String()
	if err != nil {
		return errors.New(strErr)
	}
	return nil
}

func checkExists(fpath string) bool {
	_, err := os.Stat(fpath)
	return !errors.Is(err, os.ErrNotExist)
}
