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
const youtubeApi = "https://www.googleapis.com/youtube/v3/search?part=snippet&maxResults=3&type=video&safeSearch=none"

const ytdlpFormat = "ba[acodec=opus][asr=48K][ext=webm][audio_channels=2]"

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

func play(gw *internal.Gateway, ctx context.Context, data internal.InteractionData, botAppId, ytApiKey, songFolder string) {
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
		req, err := http.NewRequest("GET", youtubeApi, nil)
		if err != nil {
			return nil, err
		}
		params := req.URL.Query()
		params.Set("q", html.EscapeString(query))
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

	// Extract song data from top result
	title := html.UnescapeString(
		results.Items[0].Snippet.Title)
	songId := results.Items[0].Id.VideoId
	songPath := songFolder + "/" + songId + ".opus"

	// Check if file already exists
	// Otherwise download it
	if !checkExists(songPath) {
		log.Println("downloading", songId)

		// Download from youtube with yt-dlp
		cmd := exec.Command(
			"conda",
			"run",
			"-n", "yt-bot-env",
			"yt-dlp",
			"-f", ytdlpFormat,
			"-o", songFolder+"/%(id)s.%(ext)s",
			songId,
		)
		stdErr := strings.Builder{}
		cmd.Stderr = &stdErr
		err = cmd.Run()
		ytdlpErr := stdErr.String()
		if err != nil || strings.Contains(ytdlpErr, "ERROR") {
			log.Println("yt-dlp err:", ytdlpErr)
			partMsg := "Failed to download "
			if strings.Contains(
				ytdlpErr,
				"format is not available",
			) {
				partMsg = "No suitable format available for "
			}
			patchResp(botAppId, data.Token, partMsg + title)
			return
		}

		// Convert to opus file
		webmPath := songFolder+"/"+songId+".webm"
		cmd = exec.Command(
			"ffmpeg",
			"-i", webmPath,
			"-map_metadata", "-1",
			"-vn",
			"-c:a", "copy",
			"-f", "opus",
			"-ar", "48000",
			"-ac", "2",
			songPath,
		)
		stdErr.Reset()
		cmd.Stderr = &stdErr
		err = cmd.Run()
		if err != nil {
			log.Println("ffmpeg err:", stdErr.String())
			patchResp(
				botAppId, data.Token,
				"Failed to convert "+title,
			)
			return
		}

		// Delete .webm file
		os.Remove(webmPath)

		// Check that file exists
		if !checkExists(songPath) {
			log.Println("file doesn't exist:", songId)
			patchResp(
				botAppId, data.Token,
				"Failed to download "+title,
			)
			return
		}

		log.Println("downloaded", songId)
	} else {
		log.Println("already have", songId)
	}

	// Try to join channel
	err = gw.JoinChannel(ctx, data.GuildId, &chnl)
	if err != nil {
		log.Println("join err:", err)
		patchResp(
			botAppId, data.Token,
			"Could not join channel",
		)
		return
	}

	// Start playing
	patchResp(botAppId, data.Token, "Playing "+title,)
	err = gw.PlayAudio(ctx, data.GuildId, songPath)
	if err != nil {
		log.Println("play error:", songId, err)
		return
	}
	log.Println("done playing", songId)
}

func stop(gw *internal.Gateway, ctx context.Context, data internal.InteractionData) {
	log.Println("stopping", data.GuildId)
	gw.StopAudio(ctx, data.GuildId)
	gw.JoinChannel(ctx, data.GuildId, nil)
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

func checkExists(fpath string) bool {
	_, err := os.Stat(fpath)
	return !errors.Is(err, os.ErrNotExist)
}
