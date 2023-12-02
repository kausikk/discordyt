package main

import (
	"encoding/json"
	"bytes"
	"net/http"
	"html"
	"fmt"
	"io"
)

const youtubeApi = "https://www.googleapis.com/youtube/v3/search"

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

const discordApi = "https://discord.com/api"

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

type interactionPost struct {
	Type interactionRespType `json:"type"`
	Data interactionContent  `json:"data"`
}
type interactionContent struct {
	Content string `json:"content"`
}

func searchYoutube(query, ytApiKey string) (*ytSearchList, error) {
	req, err := http.NewRequest("GET", youtubeApi, nil)
	if err != nil {
		return nil, err
	}
	params := req.URL.Query()
	params.Set("q", html.EscapeString(query))
	params.Set("key", ytApiKey)
	params.Set("part", "snippet")
	params.Set("type", "video")
	params.Set("maxResults", "1")
	params.Set("safeSearch", "none")
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

func fmtDuration(seconds int) string {
	if seconds >= 3600 {
		return fmt.Sprintf(
			"%d:%02d:%02d",
			seconds/3600,
			(seconds/60)%60,
			seconds%60,
		)
	}
	return fmt.Sprintf(
		"%d:%02d",
		seconds/60,
		seconds%60,
	)
}
