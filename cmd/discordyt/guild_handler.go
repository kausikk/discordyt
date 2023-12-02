package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"html"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kausikk/discordyt/internal"
)

// https://datatracker.ietf.org/doc/html/rfc3533#section-6
const pageHeaderLen = 27
const maxSegTableLen = 255

const maxSongQLen = 50
const maxSongQMsg = "Too many songs in queue (max 50)"

const inactiveTimeout = 5 * time.Minute

type songQueue struct {
	arr   [maxSongQLen]songQueueItem
	ihead int
	len   int
	itail int
}

type songid uint32

const FINDING = 0
const FOUND = 1
const NOT_FOUND = 2

type songQueueItem struct {
	id     songid
	token  string
	chnlId string
	title  string
	path   string
	found  int
}

type findResult struct {
	id    songid
	pass  bool
	title string
	path  string
}

func guildHandler(gw *internal.Gateway, ctx context.Context, cmds chan internal.InteractionData, guildId, botAppId, ytApiKey, songFolder string) {
	// Song queue for tracking all songs in-flight
	q := &songQueue{}

	// Receives results of find() and play() functions
	doneFind := make(chan findResult)
	donePlay := make(chan songid)

	// Create inactive timer and stop immediately
	timer := time.NewTimer(inactiveTimeout)
	defer timer.Stop()
	timer.Stop()

	startNextSong := func() {
		// Pop songs that could not been found until reaching
		// a song that is still finding or ready to be played
		for head := q.head(); head != nil; head = q.head() {
			if head.found == NOT_FOUND {
				q.pop()
				continue
			}
			if head.found == FINDING {
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
			go play(gw, ctx, guildId, head.path, head.id, donePlay)
			break
		}
	}

	// Start handle loop
	for {
		startInactiveTimer := true
		select {
		case cmd := <-cmds:
			switch cmd.Data.Name {
			case "play":
				if cmd.ChnlId == "" {
					postResp(
						cmd.Id, cmd.Token,
						"User is not in a channel (try re-joining)",
						channelMessageWithSource,
					)
					break
				}
				id, ok := q.push(songQueueItem{
					token: cmd.Token, chnlId: cmd.ChnlId,
				})
				// Not enough room in queue
				if !ok {
					postResp(
						cmd.Id, cmd.Token, maxSongQMsg,
						channelMessageWithSource,
					)
					break
				}
				slog.Info(
					"find start",
					"q", cmd.Data.Options[0].Value,
					"g", guildId,
					"c", cmd.ChnlId,
				)
				postResp(
					cmd.Id, cmd.Token, "Finding song...",
					deferredChannelMessageWithSource,
				)
				go find(
					ctx, cmd.Data.Options[0].Value,
					ytApiKey, songFolder,
					id, doneFind,
				)
			case "skip":
				head := q.head()
				if head == nil {
					postResp(
						cmd.Id, cmd.Token, "Nothing to skip",
						channelMessageWithSource,
					)
					break
				}
				slog.Info("skipping", "g", guildId)
				postResp(
					cmd.Id, cmd.Token, "Skipping",
					channelMessageWithSource,
				)
				gw.StopAudio(guildId)
				// Respond to interaction if song hasn't been found yet
				if head.found == FINDING {
					patchResp(botAppId, head.token, "Skipped")
				}
				q.pop()
				startNextSong()
			case "stop":
				slog.Info("stopping", "g", guildId)
				postResp(
					cmd.Id, cmd.Token, "Stopped",
					channelMessageWithSource,
				)
				gw.StopAudio(guildId)
				gw.ChangeChannel(ctx, guildId, internal.NullChannelId)
				// Respond to interaction for songs that haven't been found yet
				for head := q.head(); head != nil; head = q.head() {
					if head.found == FINDING {
						patchResp(botAppId, head.token, "Stopped")
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
			song.title = res.title
			song.path = res.path
			song.found = FOUND
			if !res.pass {
				song.found = NOT_FOUND
				patchResp(
					botAppId, song.token,
					"Could not find song",
				)
			}
			// Check if song is head and play it
			head := q.head()
			if head == nil {
				break
			}
			if head.id != song.id {
				slog.Info("queued", "p", song.path)
				if song.found == FOUND {
					patchResp(
						botAppId, song.token,
						"Added "+song.title+" to queue",
					)
				}
				break
			}
			if head.found == FOUND {
				patchResp(
					botAppId, head.token,
					"Playing "+head.title,
				)
			}
			startNextSong()
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
			startNextSong()
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

func find(ctx context.Context, query, ytApiKey, songFolder string, id songid, doneFind chan findResult) {
	// Search youtube for most relevant video
	results, err := searchYoutube(query, ytApiKey)
	if err != nil {
		slog.Error("ytapi", "e", err)
		doneFind <- findResult{
			id: id, pass: false,
		}
		return
	}

	if len(results.Items) < 1 {
		slog.Error("find no results", "q", query)
		doneFind <- findResult{
			id: id, pass: false,
		}
		return
	}

	title := html.UnescapeString(
		results.Items[0].Snippet.Title)
	songId := results.Items[0].Id.VideoId
	opusPath := songFolder + "/" + songId + ".opus"

	f, err := os.Open(opusPath)
	// Download song if it doesn't already exist
	if err != nil {
		stdOut := &strings.Builder{}
		stdErr := &strings.Builder{}

		cmd := exec.Command(
			"yt-dlp",
			"--print", "after_video:%(duration)s",
			"-f", "ba[acodec=opus][asr=48K][ext=webm][audio_channels=2]",
			"-o", songFolder+"/%(id)s.%(ext)s",
			"--", songId,
		)
		cmd.Stdout = stdOut
		cmd.Stderr = stdErr
		err := cmd.Run()
		ytdlpErr := stdErr.String()
		if err != nil || len(ytdlpErr) > 0 {
			slog.Error("find ytdlp err", "e1", ytdlpErr, "e2", err)
			doneFind <- findResult{
				id: id, pass: false,
			}
			return
		}
	
		rawdur := strings.TrimSpace(stdOut.String())
		duration, _ := strconv.Atoi(rawdur)
	
		stdOut.Reset()
		stdErr.Reset()
		webmPath := songFolder + "/" + songId + ".webm"
	
		cmd = exec.Command(
			"ffmpeg",
			"-y",
			"-i", webmPath,
			"-nostdin",
			"-nostats",
			"-loglevel", "error",
			"-map_metadata", "-1",
			// This line adds a "duration" tag
			// to the Opus file's comment header
			"-metadata", "duration="+rawdur,
			"-vn",
			"-c:a", "copy",
			"-f", "opus",
			"-ar", "48000",
			"-ac", "2",
			opusPath,
		)
		cmd.Stderr = stdErr
		err = cmd.Run()
		ffmpegErr := stdErr.String()
		if err != nil || len(ffmpegErr) > 0 {
			slog.Error("find ffmpeg err", "e1", ffmpegErr, "e2", err)
			doneFind <- findResult{
				id: id, pass: false,
			}
			return
		}
	
		os.Remove(webmPath)
	
		fmtdur := fmtDuration(duration)
		slog.Info("find downloaded", "id", songId, "dur", fmtdur)
		doneFind <- findResult{
			id, true, title + " (" + fmtdur + ")", opusPath,
		}
		return
	}
	defer f.Close()

	var duration int

	// Parse Opus pages
	// https://datatracker.ietf.org/doc/html/rfc3533#section-6
	headerBuf := [pageHeaderLen]byte{}
	segTable := [maxSegTableLen]byte{}

	for i := 0; i < 2; i++ {
		// Parse page header
		n, err := io.ReadFull(f, headerBuf[:])
		if err == io.EOF || n < pageHeaderLen {
			break
		}
		if string(headerBuf[:4]) != "OggS" {
			break
		}
		tableLen := int(headerBuf[26])
		_, err = io.ReadFull(f, segTable[:tableLen])
		if err != nil {
			break
		}
		var sum int64
		for _, v := range segTable[:tableLen] {
			sum += int64(v)
		}

		// Discard first page
		if i == 0 {
			_, err := f.Seek(sum, io.SeekCurrent)
			if err != nil {
				break
			}
			continue
		}

		// Read entire comment header
		comments := make([]byte, sum)
		_, err = io.ReadFull(f, comments)
		if err != nil {
			break
		}

		// https://datatracker.ietf.org/doc/html/rfc7845.html#autoid-19
		// Chop off magic string
		if !bytes.HasPrefix(comments, []byte("OpusTags")) {
			break
		}
		comments = comments[8:]
		// Read, then chop off vendor string length
		if len(comments) < 4 {
			break
		}
		strlen := binary.LittleEndian.Uint32(comments[:4])
		comments = comments[4:]
		// Chop off vendor string
		if len(comments) < int(strlen) {
			break
		}
		comments = comments[strlen:]
		// Read, then chop off user comment list length
		if len(comments) < 4 {
			break
		}
		numComms := binary.LittleEndian.Uint32(comments[:4])
		comments = comments[4:]

		for c := 0; c < int(numComms); c++ {
			// Read, then chop off user comment string length
			if len(comments) < 4 {
				break
			}
			strlen = binary.LittleEndian.Uint32(comments[:4])
			comments = comments[4:]
			// Check if duration, otherwise chop off and keep looping
			if len(comments) < int(strlen) {
				break
			}
			if !bytes.HasPrefix(comments, []byte("duration=")) {
				comments = comments[strlen:]
				continue
			}
			// Extract duration
			comments = comments[9:strlen]
			if len(comments) < 1 {
				break
			}
			duration, _ = strconv.Atoi(string(comments))
			break
		}
	}

	fmtdur := fmtDuration(duration)
	slog.Info("find already downloaded", "id", songId, "dur", fmtdur)
	doneFind <- findResult{
		id, true, title + " (" + fmtdur + ")", opusPath,
	}
}

func play(gw *internal.Gateway, ctx context.Context, guildId, path string, id songid, donePlay chan songid) {
	slog.Info("play start", "p", path, "g", guildId)
	err := gw.PlayAudio(ctx, guildId, path)
	if err != nil {
		slog.Error("play fail", "e", err, "g", guildId)
	} else {
		slog.Info("play done", "g", guildId)
	}
	donePlay <- id
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
