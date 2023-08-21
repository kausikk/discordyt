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

const maxSongQLen = 10
const maxSongQMsg = "Too many songs in queue (max 10)"

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

type songQueue struct {
	arr   [maxSongQLen]songQueueItem
	ihead int
	len   int
	itail int
}

type songid uint32

type songQueueItem struct {
	id     songid
	token  string
	chnlId string
	title  string
	path   string
	found  bool
	skip   bool
}

type findResult struct {
	id    songid
	pass  bool
	title string
	path  string
}

func guildCmdHandler(gw *internal.Gateway, ctx context.Context, cmd chan internal.InteractionData, guildId, botAppId, ytApiKey, songFolder string) {
	q := &songQueue{}
	doneFind := make(chan findResult, 5)
	donePlay := make(chan songid, 5)
	for {
		select {
		case data := <-cmd:
			switch data.Data.Name {
			case "play":
				// Check if user is in a channel
				if data.ChnlId == "" {
					postResp(
						data.Id, data.Token,
						"User is not in a channel (try re-joining)",
						channelMessageWithSource,
					)
					break
				}
				// Add song to queue
				id, ok := q.push(songQueueItem{
					token: data.Token, chnlId: data.ChnlId,
				})
				// Not enough room in queue
				if !ok {
					postResp(
						data.Id, data.Token, maxSongQMsg,
						channelMessageWithSource,
					)
					break
				}
				// Start finding song
				postResp(
					data.Id, data.Token, "Finding song...",
					deferredChannelMessageWithSource,
				)
				go find(
					ctx, data.Data.Options[0].Value,
					ytApiKey, songFolder,
					id, doneFind,
				)
			case "stop":
				// Stop current audio and leave channel
				postResp(
					data.Id, data.Token, "Stopped",
					channelMessageWithSource,
				)
				gw.StopAudio(ctx, guildId)
				gw.JoinChannel(ctx, guildId, internal.NullChannelId)
				// Respond to interaction for songs
				// that have not yet been found
				for head := q.head(); head != nil; head = q.head() {
					if !head.found {
						patchResp(
							botAppId, head.token,
							"Stopped",
						)
					}
					q.pop()
				}
			}
		case res := <-doneFind:
			// Store results in song
			song := q.find(res.id)
			if song == nil {
				break
			}
			song.found = true
			song.skip = !res.pass
			song.title = res.title
			song.path = res.path
			// Play the song if it's head of queue
			head := q.head()
			if head.id == song.id {
				// Check if song couldn't be found
				if head.skip {
					patchResp(
						botAppId, head.token,
						"Could not find song",
					)
					// Send to donePlay so that next
					// songs can attempt to be played
					donePlay <- head.id
					break
				}
				// Try to join channel
				err := gw.JoinChannel(ctx, guildId, head.chnlId)
				if err != nil {
					patchResp(
						botAppId, head.token,
						"Could not join channel",
					)
					// Send to donePlay so that next
					// songs can attempt to be played
					donePlay <- head.id
					break
				}
				// Play song
				patchResp(
					botAppId, head.token,
					"Playing "+head.title,
				)
				go func() {
					err := gw.PlayAudio(ctx, guildId, head.path)
					if err != nil {
						log.Println("play err:", err)
					}
					donePlay <- head.id
				}()
			} else {
				// Tell user that it's added to queue
				patchResp(
					botAppId, song.token,
					"Added "+song.title+" to queue",
				)
			}
		case id := <-donePlay:
			// Pop the song if it's still the head
			head := q.head()
			if head == nil {
				break
			}
			if head.id != id {
				break
			}
			q.pop()
			// Pop songs that have been found
			// and marked for skipping until
			// reaching a song that can be played
			// or hasn't been found yet
			for head = q.head(); head != nil; head = q.head() {
				if head.skip {
					q.pop()
					continue
				}
				if !head.found {
					break
				}
				// Try to join channel
				err := gw.JoinChannel(ctx, guildId, head.chnlId)
				if err != nil {
					q.pop()
					continue
				}
				// Play next song
				go func() {
					err := gw.PlayAudio(ctx, guildId, head.path)
					if err != nil {
						log.Println("play err:", err)
					}
					donePlay <- head.id
				}()
				break
			}
		case <-ctx.Done():
			return
		}
	}
}

func find(ctx context.Context, query, ytApiKey, songFolder string, id songid, done chan findResult) {
	// Search youtube for most relevant video
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
		done <- findResult{
			id: id, pass: false,
		}
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
		err := func() error {
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
				return err
			}

			// Convert to opus file
			webmPath := songFolder + "/" + songId + ".webm"
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
				return err
			}

			// Delete .webm file
			os.Remove(webmPath)
			return nil
		}()

		// Check that file exists
		if err != nil || !checkExists(songPath) {
			log.Println("file doesn't exist:", songId)
			done <- findResult{
				id: id, pass: false,
			}
			return
		}

		log.Println("downloaded", songId)
	} else {
		log.Println("already have", songId)
	}

	done <- findResult{
		id, true, title, songPath,
	}
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

var _uuid songid

func (q *songQueue) push(item songQueueItem) (songid, bool) {
	if q.len == maxSongQLen {
		return 0, false
	}
	_uuid++
	q.len++
	q.arr[q.itail] = item
	q.arr[q.itail].id = _uuid
	q.itail = (q.itail + 1) % maxSongQLen
	return _uuid, true
}

func (q *songQueue) pop() {
	if q.len == 0 {
		return
	}
	q.len--
	q.ihead = (q.ihead + 1) % maxSongQLen
}

func (q *songQueue) find(id songid) *songQueueItem {
	for i := 0; i < q.len; i++ {
		ind := (q.ihead + i) % maxSongQLen
		if q.arr[ind].id == id {
			return &q.arr[ind]
		}
	}
	return nil
}

func (q *songQueue) head() *songQueueItem {
	if q.len == 0 {
		return nil
	}
	return &q.arr[q.ihead]
}
