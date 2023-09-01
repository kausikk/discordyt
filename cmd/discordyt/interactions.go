package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kausikk/discordyt/internal"
)

const discordApi = "https://discord.com/api"
const youtubeApi = "https://www.googleapis.com/youtube/v3/search?part=snippet&maxResults=3&type=video&safeSearch=none"

const ytdlpFormat = "ba[acodec=opus][asr=48K][ext=webm][audio_channels=2]"

const maxSongQLen = 50
const maxSongQMsg = "Too many songs in queue (max 50)"

const inactiveTimeout = 5 * time.Minute

type interactionRespType int

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
	doneFind := make(chan findResult)
	donePlay := make(chan songid)

	play := func(path string, id songid) {
		slog.Info("play start", "p", path, "g", guildId)
		err := gw.PlayAudio(ctx, guildId, path)
		if err != nil {
			slog.Error("play fail", "e", err, "g", guildId)
		} else {
			slog.Info("play done", "g", guildId)
		}
		donePlay <- id
	}

	// Create inactive timer and stop immediately
	timer := time.NewTimer(inactiveTimeout)
	defer timer.Stop()
	timer.Stop()

	// Start handle loop
	for {
		startInactiveTimer := true
		select {
		case data := <-cmd:
			switch data.Data.Name {
			case "play":
				if data.ChnlId == "" {
					postResp(
						data.Id, data.Token,
						"User is not in a channel (try re-joining)",
						channelMessageWithSource,
					)
					break
				}
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
				slog.Info(
					"find start",
					"q", data.Data.Options[0].Value,
					"g", guildId,
					"c", data.ChnlId,
				)
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
				slog.Info("stopping", "g", guildId)
				postResp(
					data.Id, data.Token, "Stopped",
					channelMessageWithSource,
				)
				gw.StopAudio(guildId)
				gw.ChangeChannel(ctx, guildId, internal.NullChannelId)
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
			slog.Info(
				"find done",
				"p", res.path,
				"g", guildId,
			)
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
				err := gw.ChangeChannel(ctx, guildId, head.chnlId)
				if err != nil {
					slog.Error(
						"join fail",
						"g", guildId,
						"c", head.chnlId,
						"e", err,
					)
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
				go play(head.path, head.id)
			} else {
				slog.Info("queued", "p", song.path)
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
				err := gw.ChangeChannel(ctx, guildId, head.chnlId)
				if err != nil {
					slog.Error(
						"join fail",
						"g", guildId,
						"c", head.chnlId,
						"e", err,
					)
					q.pop()
					continue
				}
				// Play next song
				go play(head.path, head.id)
				break
			}
		case <-timer.C:
			// Check if there is a song in the queue
			head := q.head()
			if head != nil {
				break
			}
			// Stop inactive timer
			slog.Info("inactive leave", "g", guildId)
			startInactiveTimer = false
			// No cmds or downloads were recently complete,
			// so leave the voice channnel
			err := gw.ChangeChannel(ctx, guildId, internal.NullChannelId)
			if err != nil {
				slog.Error(
					"join fail",
					"g", guildId,
					"c", internal.NullChannelId,
					"e", err,
				)
			}
		case <-ctx.Done():
			return
		}
		timer.Stop()
		if startInactiveTimer {
			timer.Reset(inactiveTimeout)
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
		slog.Error("ytapi", "e", err)
		done <- findResult{
			id: id, pass: false,
		}
		return
	}

	if len(results.Items) < 1 {
		slog.Error("find no results", "q", query)
		done <- findResult{
			id: id, pass: false,
		}
		return
	}
	title := html.UnescapeString(
		results.Items[0].Snippet.Title)
	songId := results.Items[0].Id.VideoId

	songPath, dur, ok := search(songFolder, songId, "opus")
	if !ok {
		err := func() error {
			cmd := exec.Command(
				"yt-dlp",
				"-f", ytdlpFormat,
				"-o", songFolder+"/%(id)s-%(duration)s.%(ext)s",
				songId,
			)
			stdErr := strings.Builder{}
			cmd.Stderr = &stdErr
			err = cmd.Run()
			ytdlpErr := stdErr.String()
			if err != nil || strings.Contains(ytdlpErr, "ERROR") {
				slog.Error("yt-dlp", "e", ytdlpErr)
				return err
			}

			webmPath, dur, ok := search(songFolder, songId, "webm")
			if !ok {
				slog.Error("find no webm")
				return errors.New("could not find webm file")
			}

			// Make duration human-readable
			sec, err := strconv.Atoi(dur)
			if err != nil {
				slog.Error("find invalid duration")
				return errors.New("invalid duration")
			}
			if sec >= 360 {
				dur = fmt.Sprintf(
					"%d:%02d:%02d",
					sec/3600,
					(sec/60)%60,
					sec%60,
				)
			} else {
				dur = fmt.Sprintf(
					"%d:%02d",
					sec/60,
					sec%60,
				)
			}

			opusPath := songFolder + "/" + songId + "-" + dur + ".opus"
			cmd = exec.Command(
				"ffmpeg",
				"-i", webmPath,
				"-map_metadata", "-1",
				"-vn",
				"-c:a", "copy",
				"-f", "opus",
				"-ar", "48000",
				"-ac", "2",
				opusPath,
			)
			stdErr.Reset()
			cmd.Stderr = &stdErr
			err = cmd.Run()
			if err != nil {
				slog.Error("ffmpeg", "e", stdErr.String())
				return err
			}

			os.Remove(webmPath)
			return nil
		}()

		songPath, dur, ok = search(songFolder, songId, "opus")
		if err != nil || !ok {
			slog.Error("find no opus", "e", err)
			done <- findResult{
				id: id, pass: false,
			}
			return
		}

		slog.Info("find downloaded", "id", songId)
	} else {
		slog.Info("find already had", "id", songId)
	}

	done <- findResult{
		id, true, title + " (" + dur + ")", songPath,
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

func search(songFolder, songId, ext string) (string, string, bool) {
	f, err := os.Open(songFolder)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	names, err := f.Readdirnames(0)
	if err != nil {
		return "", "", false
	}

	// Looking for a filename of the form:
	// SONGID + '-' + DURATION + '.' + EXT
	nId := len(songId)
	nExt := len(ext)
	minLen := nId + 2 + nExt
	for _, name := range names {
		nName := len(name)
		if nName < minLen {
			continue
		}
		if name[:nId] != songId {
			continue
		}
		if name[nName-nExt:nName] != ext {
			continue
		}
		return songFolder + "/" + name,
			name[nId+1 : nName-nExt-1],
			true
	}
	return "", "", false
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
